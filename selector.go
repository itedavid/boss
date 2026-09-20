//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// 框选期间使用的全局状态（单线程、模态框选，安全）。
var (
	virtX, virtY, virtW, virtH int // 整个虚拟屏幕（含多显示器）的边界
	overlayHwnd                HWND
	overlayClassReady          bool
	memDC                      uintptr
	hbmp                       uintptr
	dibBits                    *byte // 32 位 BGRA 像素缓冲区
	dragging                   bool
	startPt                    POINT
	endPt                      POINT
	selResult                  Rect
	selOK                      bool
	selFinished                bool
)

// 遮罩与选框的透明度（0-255）
const (
	maskAlpha   = 40  // 选区之外：整屏半透明黑色遮罩
	fillAlpha   = 30  // 选区内部：淡红
	borderAlpha = 200 // 选区边框
	minSelSize  = 2   // 小于该尺寸视为误点，按取消处理
)

// selectRegion 进入「框选区域」模式：创建一个覆盖整个虚拟屏幕的透明顶层窗口，
// 用户按住左键拖动即可框选，松开后返回矩形（left/top 取 min，right/bottom 取 max）。
// hideWindow 一般是主窗口：等叠加层拿到焦点之后再隐藏它，避免中途把前台让给别的程序。
// 返回 (Rect{}, false) 表示取消（按 Esc、框选区域过小或出错）。
func selectRegion(hideWindow HWND) (Rect, bool) {
	virtX = getSystemMetrics(SM_XVIRTUALSCREEN)
	virtY = getSystemMetrics(SM_YVIRTUALSCREEN)
	virtW = getSystemMetrics(SM_CXVIRTUALSCREEN)
	virtH = getSystemMetrics(SM_CYVIRTUALSCREEN)
	if virtW <= 0 || virtH <= 0 {
		// 取不到虚拟屏幕时退化到主显示器
		virtX = 0
		virtY = 0
		virtW = getSystemMetrics(SM_CXSCREEN)
		virtH = getSystemMetrics(SM_CYSCREEN)
	}
	if virtW <= 0 || virtH <= 0 {
		return Rect{}, false
	}

	dragging = false
	selResult = Rect{}
	selOK = false
	selFinished = false

	hinst := getModuleHandle()
	if !overlayClassReady {
		wc := WNDCLASSEX{
			Size:      uint32(unsafe.Sizeof(WNDCLASSEX{})),
			WndProc:   syscall.NewCallback(overlayWndProc),
			Instance:  uintptr(hinst),
			ClassName: utf16ptr("BossSelectClass"),
			Cursor:    loadCursor(0, IDC_ARROW),
		}
		registerClassEx(&wc)
		overlayClassReady = true
	}

	overlayHwnd = createWindowEx(
		WS_EX_LAYERED|WS_EX_TOPMOST,
		utf16ptr("BossSelectClass"),
		nil,
		WS_POPUP,
		int32(virtX), int32(virtY), int32(virtW), int32(virtH),
		0, 0, hinst, 0)
	if overlayHwnd == 0 {
		return Rect{}, false
	}
	if memDC == 0 || hbmp == 0 || dibBits == nil {
		// 后台位图创建失败，直接收工，避免留下一个点不动的透明窗口
		destroyWindow(overlayHwnd)
		overlayHwnd = 0
		return Rect{}, false
	}

	showWindow(overlayHwnd, SW_SHOW)
	setForegroundWindow(overlayHwnd)
	setFocus(overlayHwnd)
	if hideWindow != 0 {
		showWindow(hideWindow, SW_HIDE)
	}
	drawSelection() // 先铺一层全屏遮罩，让用户知道已经进入框选模式

	// 模态消息循环：直到框选结束（selFinished）或收到 WM_QUIT。
	var msg MSG
	for {
		if getMessage(&msg) == 0 {
			break
		}
		translateMessage(&msg)
		dispatchMessage(&msg)
		if selFinished {
			break
		}
	}
	if overlayHwnd != 0 {
		destroyWindow(overlayHwnd)
		overlayHwnd = 0
	}
	return selResult, selOK
}

func overlayWndProc(hwnd HWND, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		createOverlayBitmap()
		return 0

	case WM_LBUTTONDOWN:
		setCapture(hwnd)
		dragging = true
		startPt = clientToScreenPoint(lparam)
		endPt = startPt
		drawSelection()
		return 0

	case WM_MOUSEMOVE:
		if dragging {
			endPt = clientToScreenPoint(lparam)
			drawSelection()
		}
		return 0

	case WM_LBUTTONUP:
		if dragging {
			dragging = false
			releaseCapture()
			r := normalizeRect(startPt, endPt)
			if r.Right-r.Left >= minSelSize && r.Bottom-r.Top >= minSelSize {
				selResult = r
				selOK = true
			}
			selFinished = true
			destroyWindow(hwnd)
		}
		return 0

	case WM_KEYDOWN:
		if wparam == VK_ESCAPE {
			dragging = false
			selOK = false
			selFinished = true
			destroyWindow(hwnd)
		}
		return 0

	case WM_DESTROY:
		releaseOverlayBitmap()
		return 0
	}
	return defWindowProc(hwnd, msg, wparam, lparam)
}

// clientToScreenPoint 把鼠标消息里的 client 坐标换算成屏幕物理坐标。
// 叠加窗口正好贴在虚拟屏幕原点，client 坐标加上原点偏移即可。
func clientToScreenPoint(lparam uintptr) POINT {
	x := int(int16(uint16(lparam)))
	y := int(int16(uint16(lparam >> 16)))
	return POINT{X: int32(virtX + x), Y: int32(virtY + y)}
}

// normalizeRect 不假设用户一定从左上往右下拖，统一取 min/max。
func normalizeRect(a, b POINT) Rect {
	return Rect{
		Left:   min(int(a.X), int(b.X)),
		Top:    min(int(a.Y), int(b.Y)),
		Right:  max(int(a.X), int(b.X)),
		Bottom: max(int(a.Y), int(b.Y)),
	}
}

func createOverlayBitmap() {
	memDC = createCompatibleDC(0)
	if memDC == 0 {
		return
	}
	bmi := BITMAPINFO{}
	bmi.Header.Size = uint32(unsafe.Sizeof(BITMAPINFOHEADER{}))
	bmi.Header.Width = int32(virtW)
	bmi.Header.Height = int32(-virtH) // 负数 = 自上而下的位图
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = BI_RGB

	var pbits unsafe.Pointer
	hbmp = createDIBSection(memDC, &bmi, DIB_RGB_COLORS, &pbits, 0, 0)
	if hbmp != 0 {
		selectObject(memDC, hbmp)
		dibBits = (*byte)(pbits)
	}
}

func releaseOverlayBitmap() {
	if memDC != 0 {
		deleteDC(memDC)
		memDC = 0
	}
	if hbmp != 0 {
		deleteObject(hbmp)
		hbmp = 0
	}
	dibBits = nil
	overlayHwnd = 0
}

// setPx 写入一个红像素（BGRA 预乘：B=0, G=0, R=a, A=a）。
func setPx(bits []byte, w, h, x, y, a int) {
	if x < 0 || y < 0 || x >= w || y >= h {
		return
	}
	idx := (y*w + x) * 4
	bits[idx] = 0
	bits[idx+1] = 0
	bits[idx+2] = uint8(a)
	bits[idx+3] = uint8(a)
}

// drawSelection 重绘叠加层：整屏半透明黑遮罩，选区内淡红，外加 2px 红边。
func drawSelection() {
	if dibBits == nil || virtW <= 0 || virtH <= 0 {
		return
	}
	n := virtW * virtH * 4
	bits := unsafe.Slice(dibBits, n)
	for i := 0; i < n; i += 4 {
		bits[i] = 0
		bits[i+1] = 0
		bits[i+2] = 0
		bits[i+3] = maskAlpha
	}
	if dragging {
		r := normalizeRect(startPt, endPt)
		// 换算成缓冲区（虚拟屏幕）内的坐标，多显示器下原点可能不在 (0,0)
		x0 := r.Left - virtX
		y0 := r.Top - virtY
		x1 := r.Right - virtX
		y1 := r.Bottom - virtY
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				setPx(bits, virtW, virtH, x, y, fillAlpha)
			}
		}
		for x := x0; x < x1; x++ {
			setPx(bits, virtW, virtH, x, y0, borderAlpha)
			setPx(bits, virtW, virtH, x, y0+1, borderAlpha)
			setPx(bits, virtW, virtH, x, y1-1, borderAlpha)
			setPx(bits, virtW, virtH, x, y1-2, borderAlpha)
		}
		for y := y0; y < y1; y++ {
			setPx(bits, virtW, virtH, x0, y, borderAlpha)
			setPx(bits, virtW, virtH, x0+1, y, borderAlpha)
			setPx(bits, virtW, virtH, x1-1, y, borderAlpha)
			setPx(bits, virtW, virtH, x1-2, y, borderAlpha)
		}
	}
	updateLayered()
}

// updateLayered 通过 UpdateLayeredWindow 把内存位图推到透明顶层窗口。
func updateLayered() {
	if overlayHwnd == 0 || memDC == 0 {
		return
	}
	ptDst := POINT{X: int32(virtX), Y: int32(virtY)}
	sz := SIZE{CX: int32(virtW), CY: int32(virtH)}
	ptSrc := POINT{X: 0, Y: 0}
	blend := BLENDFUNCTION{
		BlendOp:             AC_SRC_OVER,
		SourceConstantAlpha: 255,
		AlphaFormat:         AC_SRC_ALPHA,
	}
	updateLayeredWindow(overlayHwnd, 0, &ptDst, &sz, memDC, &ptSrc, 0, &blend, ULW_ALPHA)
}
