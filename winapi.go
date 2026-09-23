//go:build windows

// winapi.go 用 Go 标准库 syscall 直接调用 Win32 API，避免任何第三方依赖。
package main

import (
	"syscall"
	"unsafe"
)

// ----- 句柄类型 -----
type HWND uintptr
type HINSTANCE uintptr
type HMENU uintptr

// ----- 常量（Windows 稳定值）-----
const (
	WM_CREATE           = 0x0001
	WM_DESTROY          = 0x0002
	WM_COMMAND          = 0x0111
	WM_KEYDOWN          = 0x0100
	WM_LBUTTONDOWN      = 0x0201
	WM_LBUTTONUP        = 0x0202
	WM_MOUSEMOVE        = 0x0200
	WM_SETFONT          = 0x0030
	WM_QUIT             = 0x0012
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_CHILD            = 0x40000000
	WS_VISIBLE          = 0x10000000
	WS_POPUP            = 0x80000000
	WS_EX_LAYERED       = 0x00080000
	WS_EX_TOPMOST       = 0x00000008
	WS_EX_CLIENTEDGE    = 0x00000200
	BS_PUSHBUTTON       = 0x00000000
	ES_MULTILINE        = 0x0004
	ES_READONLY         = 0x0800
	ES_AUTOHSCROLL      = 0x0080
	ES_RIGHT            = 0x0002
	WS_VSCROLL          = 0x00200000
	ES_AUTOVSCROLL      = 0x0040
	SW_SHOW             = 5
	SW_HIDE             = 0
	IDC_ARROW           = 32512
	DEFAULT_GUI_FONT    = 17
	SM_XVIRTUALSCREEN   = 76
	SM_YVIRTUALSCREEN   = 77
	SM_CXVIRTUALSCREEN  = 78
	SM_CYVIRTUALSCREEN  = 79
	SM_CXSCREEN         = 0
	SM_CYSCREEN         = 1
	BI_RGB              = 0
	DIB_RGB_COLORS      = 0
	AC_SRC_OVER         = 0
	AC_SRC_ALPHA        = 1
	ULW_ALPHA           = 2
	VK_ESCAPE           = 0x1B
	VK_CONTROL          = 0x11 // Ctrl（左右通用）
	VK_C                = 0x43 // 字母 C
	COLOR_BTNFACE       = 15
	WM_CTLCOLORSTATIC   = 0x0138
	SS_LEFT             = 0x00000000
	SS_RIGHT            = 0x00000002
	SS_NOTIFY           = 0x00000100 // 静态控件被点击时向父窗口发 STN_CLICKED（注意是 0x100，不是 0x1）
	TRANSPARENT         = 1          // SetBkMode：文字背景透明（避免标签出现白底方块）
	SRCCOPY             = 0x00CC0020
	EM_SETSEL           = 0x00B1
	EM_SCROLLCARET      = 0x00B7
	EM_SETLIMITTEXT     = 0x00C5 // EDIT 最多允许输入的字符数
	ES_NUMBER           = 0x2000 // EDIT 只接受数字
	EN_CHANGE           = 0x0300 // EDIT 内容改变通知
	SWP_NOSIZE          = 0x0001 // SetWindowPos：保留当前尺寸
	SWP_NOMOVE          = 0x0002 // SetWindowPos：保留当前位置
)

// 两个特殊的 HWND 插入点：置顶 / 取消置顶。Win32 定义为 (HWND)-1 / (HWND)-2。
var (
	hwndTopMost   = ^uintptr(0)     // HWND_TOPMOST = (HWND)-1
	hwndNoTopMost = ^uintptr(0) - 1 // HWND_NOTOPMOST = (HWND)-2
)

// ----- 结构（字段顺序/类型必须与 Windows 原生布局一致）-----
type POINT struct {
	X int32
	Y int32
}

type SIZE struct {
	CX int32
	CY int32
}

type MSG struct {
	Hwnd    HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

type WNDCLASSEX struct {
	Size        uint32
	Style       uint32
	WndProc     uintptr
	ClsExtra    int32
	WndClsExtra int32
	Instance    uintptr
	Icon        uintptr
	Cursor      uintptr
	Background  uintptr
	MenuName    *uint16
	ClassName   *uint16
	IconSm      uintptr
}

type RGBQUAD struct {
	B        byte
	G        byte
	R        byte
	Reserved byte
}

type BITMAPINFOHEADER struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type BITMAPINFO struct {
	Header BITMAPINFOHEADER
	Colors [1]RGBQUAD
}

type BLENDFUNCTION struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

// ----- DLL / 过程缓存 -----
var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modGdi32    = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassExW          = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW           = modUser32.NewProc("CreateWindowExW")
	procShowWindow                = modUser32.NewProc("ShowWindow")
	procUpdateWindow              = modUser32.NewProc("UpdateWindow")
	procGetMessageW               = modUser32.NewProc("GetMessageW")
	procTranslateMessage          = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW          = modUser32.NewProc("DispatchMessageW")
	procDefWindowProcW            = modUser32.NewProc("DefWindowProcW")
	procPostQuitMessage           = modUser32.NewProc("PostQuitMessage")
	procLoadCursorW               = modUser32.NewProc("LoadCursorW")
	procDestroyWindow             = modUser32.NewProc("DestroyWindow")
	procSetWindowTextW            = modUser32.NewProc("SetWindowTextW")
	procGetWindowTextW            = modUser32.NewProc("GetWindowTextW")
	procSendMessageW              = modUser32.NewProc("SendMessageW")
	procGetSystemMetrics          = modUser32.NewProc("GetSystemMetrics")
	procUpdateLayeredWindow       = modUser32.NewProc("UpdateLayeredWindow")
	procSetProcessDpiAwarenessCtx = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = modUser32.NewProc("SetProcessDPIAware")
	procSetFocus                  = modUser32.NewProc("SetFocus")
	procSetForegroundWindow       = modUser32.NewProc("SetForegroundWindow")
	procSetCapture                = modUser32.NewProc("SetCapture")
	procReleaseCapture            = modUser32.NewProc("ReleaseCapture")
	procGetSysColorBrush          = modUser32.NewProc("GetSysColorBrush")
	procAdjustWindowRect          = modUser32.NewProc("AdjustWindowRect")
	procSetWindowPos              = modUser32.NewProc("SetWindowPos")
	procGetAsyncKeyState          = modUser32.NewProc("GetAsyncKeyState")

	procMessageBoxW = modUser32.NewProc("MessageBoxW")

	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")

	procGetStockObject     = modGdi32.NewProc("GetStockObject")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procBitBlt             = modGdi32.NewProc("BitBlt")

	procGetDC            = modUser32.NewProc("GetDC")
	procReleaseDC        = modUser32.NewProc("ReleaseDC")
	procCreateDIBSection = modGdi32.NewProc("CreateDIBSection")
	procSelectObject     = modGdi32.NewProc("SelectObject")
	procDeleteDC         = modGdi32.NewProc("DeleteDC")
	procDeleteObject     = modGdi32.NewProc("DeleteObject")
	procCreateFontW      = modGdi32.NewProc("CreateFontW")
	procSetBkMode        = modGdi32.NewProc("SetBkMode")
)

// MessageBox 图标 / 按钮常量（只用到出错提示这一个）。
const (
	mbOK        = 0x00000000
	mbIconError = 0x00000010 // MB_ICONERROR
)

// messageBox 弹一个系统消息框。用于崩溃提示：即使窗口已经没了也能弹出来。
func messageBox(hwnd HWND, text, caption string, flags uint32) int {
	r, _, _ := procMessageBoxW.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(utf16ptr(text))),
		uintptr(unsafe.Pointer(utf16ptr(caption))),
		uintptr(flags|mbOK))
	return int(r)
}

// utf16ptr 生成 Windows API 所需的 UTF-16 字符串指针（支持中文）。
func utf16ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// ----- API 封装 -----
func setDPIAware() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		procSetProcessDpiAwarenessCtx.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

func getModuleHandle() HINSTANCE {
	r, _, _ := procGetModuleHandleW.Call(0)
	return HINSTANCE(r)
}

func registerClassEx(wcx *WNDCLASSEX) uint16 {
	r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wcx)))
	return uint16(r)
}

func createWindowEx(exStyle uint32, className, windowName *uint16, style uint32,
	x, y, w, h int32, parent HWND, menu HMENU, inst HINSTANCE, param uintptr) HWND {
	r, _, _ := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), uintptr(menu), uintptr(inst), param)
	return HWND(r)
}

func showWindow(hwnd HWND, cmd int) {
	procShowWindow.Call(uintptr(hwnd), uintptr(cmd))
}

func updateWindow(hwnd HWND) {
	procUpdateWindow.Call(uintptr(hwnd))
}

// setWindowPos 包 Win32 SetWindowPos。这里只用它来切换置顶：
// insertAfter 传 hwndTopMost / hwndNoTopMost，配合 SWP_NOMOVE|SWP_NOSIZE 即不改变位置与尺寸。
func setWindowPos(hwnd HWND, insertAfter uintptr, x, y, cx, cy int32, flags uint32) bool {
	r, _, _ := procSetWindowPos.Call(
		uintptr(hwnd), insertAfter,
		uintptr(x), uintptr(y), uintptr(cx), uintptr(cy), uintptr(flags))
	return r != 0
}

// getAsyncKeyState 查某个虚拟键当前的物理按下状态。
// 返回值最高位为 1 表示"正按着"（Go 里 int16 为负）。
func getAsyncKeyState(vk int) int16 {
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return int16(r)
}

// ctrlDown 判断 Ctrl 是否正被按住（左右 Ctrl 都算）。
func ctrlDown() bool {
	return getAsyncKeyState(VK_CONTROL) < 0
}

func getMessage(msg *MSG) int {
	r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(msg)), 0, 0, 0)
	return int(r)
}

func translateMessage(msg *MSG) {
	procTranslateMessage.Call(uintptr(unsafe.Pointer(msg)))
}

func dispatchMessage(msg *MSG) {
	procDispatchMessageW.Call(uintptr(unsafe.Pointer(msg)))
}

func defWindowProc(hwnd HWND, msg uint32, wparam, lparam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}

func postQuitMessage(code int) {
	procPostQuitMessage.Call(uintptr(code))
}

func loadCursor(instance HINSTANCE, cursorID uint16) uintptr {
	r, _, _ := procLoadCursorW.Call(uintptr(instance), uintptr(cursorID))
	return r
}

func destroyWindow(hwnd HWND) bool {
	r, _, _ := procDestroyWindow.Call(uintptr(hwnd))
	return r != 0
}

func setWindowText(hwnd HWND, text *uint16) {
	procSetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(text)))
}

// getWindowText 读出控件上的文字（截断到 maxCount-1 个字符）。
func getWindowText(hwnd HWND, buf *uint16, maxCount int) int {
	r, _, _ := procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(buf)), uintptr(maxCount))
	return int(r)
}

// scrollEditToEnd 把只读 EDIT 的光标放到末尾，让它自动滚到最新一行。
func scrollEditToEnd(hwnd HWND) {
	if hwnd == 0 {
		return
	}
	end := ^uintptr(0) // -1
	sendMessage(hwnd, EM_SETSEL, end, end)
	sendMessage(hwnd, EM_SCROLLCARET, 0, 0)
}

func sendMessage(hwnd HWND, msg uint32, wparam, lparam uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}

func getSystemMetrics(index int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

func getStockObject(index int) uintptr {
	r, _, _ := procGetStockObject.Call(uintptr(index))
	return r
}

// createFont 创建一个指定像素高度的字体；height 用负值表示字符高度（像素，例如 -19 ≈ 14pt）。
// face 传 "" 使用系统默认字体；传具体字族（如 "Microsoft YaHei"）可确保中文正常显示。
func createFont(height int, face string) uintptr {
	r, _, _ := procCreateFontW.Call(
		uintptr(height), // cHeight（负=像素高度）
		0,               // cWidth
		0,               // cEscapement
		0,               // cOrientation
		0,               // cWeight（0=默认 FW_DONTCARE）
		0,               // bItalic
		0,               // bUnderline
		0,               // bStrikeOut
		1,               // iCharSet（ANSI_CHARSET=1）
		0,               // iOutPrecision
		0,               // iClipPrecision
		0,               // iQuality
		0,               // iPitchAndFamily
		uintptr(unsafe.Pointer(utf16ptr(face))),
	)
	return r
}

// setBkMode 设置 DC 的文字背景模式（TRANSPARENT=1 让文字背景透明）。
func setBkMode(hdc uintptr, mode int) int {
	r, _, _ := procSetBkMode.Call(hdc, uintptr(mode))
	return int(int32(r))
}

func createCompatibleDC(hdc uintptr) uintptr {
	r, _, _ := procCreateCompatibleDC.Call(hdc)
	return r
}

func createDIBSection(hdc uintptr, bmi *BITMAPINFO, usage uint32, bits *unsafe.Pointer, offset uintptr, flags uint32) uintptr {
	r, _, _ := procCreateDIBSection.Call(
		hdc, uintptr(unsafe.Pointer(bmi)), uintptr(usage),
		uintptr(unsafe.Pointer(bits)), offset, uintptr(flags))
	return r
}

func selectObject(hdc, obj uintptr) uintptr {
	r, _, _ := procSelectObject.Call(hdc, obj)
	return r
}

func deleteDC(hdc uintptr) {
	procDeleteDC.Call(hdc)
}

func deleteObject(obj uintptr) {
	procDeleteObject.Call(obj)
}

func getDC(hwnd HWND) uintptr {
	r, _, _ := procGetDC.Call(uintptr(hwnd))
	return r
}

func releaseDC(hwnd HWND, hdc uintptr) {
	procReleaseDC.Call(uintptr(hwnd), hdc)
}

// bitBlt 把源 DC 上的一块矩形拷到目标 DC。
func bitBlt(hdcDst uintptr, xDst, yDst, w, h int, hdcSrc uintptr, xSrc, ySrc int, rop uint32) bool {
	r, _, _ := procBitBlt.Call(hdcDst, uintptr(xDst), uintptr(yDst), uintptr(w), uintptr(h),
		hdcSrc, uintptr(xSrc), uintptr(ySrc), uintptr(rop))
	return r != 0
}

func updateLayeredWindow(hwnd HWND, hdcDst uintptr, pptDst *POINT, psize *SIZE,
	hdcSrc uintptr, pptSrc *POINT, crKey uint32, pblend *BLENDFUNCTION, flags uint32) {
	procUpdateLayeredWindow.Call(
		uintptr(hwnd), hdcDst,
		uintptr(unsafe.Pointer(pptDst)), uintptr(unsafe.Pointer(psize)),
		hdcSrc, uintptr(unsafe.Pointer(pptSrc)),
		uintptr(crKey), uintptr(unsafe.Pointer(pblend)), uintptr(flags))
}

func setFocus(hwnd HWND) {
	procSetFocus.Call(uintptr(hwnd))
}

func setForegroundWindow(hwnd HWND) {
	procSetForegroundWindow.Call(uintptr(hwnd))
}

func setCapture(hwnd HWND) {
	procSetCapture.Call(uintptr(hwnd))
}

func releaseCapture() {
	procReleaseCapture.Call()
}

// getSysColorBrush 返回系统颜色画刷（系统缓存对象，不要 DeleteObject）。
func getSysColorBrush(index int) uintptr {
	r, _, _ := procGetSysColorBrush.Call(uintptr(index))
	return r
}

// adjustWindowRect 把「想要的客户区大小」换算成「窗口大小」，
// 这样窗口尺寸不用靠猜标题栏和边框占多少像素。
func adjustWindowRect(r *RECT, style uint32, menu bool) bool {
	m := uintptr(0)
	if menu {
		m = 1
	}
	ret, _, _ := procAdjustWindowRect.Call(uintptr(unsafe.Pointer(r)), uintptr(style), m)
	return ret != 0
}
