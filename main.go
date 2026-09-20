//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	idBtnExit       = 1003
	idBtnStart      = 1004
	idBtnStop       = 1005
	idBtnClearClks  = 1008
	idBtnSelName    = 1009
	idBtnTestName   = 1010
	idBtnClearName  = 1011
	idBtnSelOnline  = 1012
	idBtnTestOnline = 1013
	idBtnClearOnl   = 1014
	idBtnStartDet   = 1015
	idBtnStopDet    = 1016
	idBtnClearLog   = 1017
	idBtnAuto       = 1018
	idBtnAutoStop   = 1019
	idBtnAutoName   = 1020
	idBtnForce      = 1021
	idBtnForceStop  = 1022
)

// 界面尺寸（像素）
const (
	btnW     = 88  // 普通按钮宽
	btnH     = 28  // 按钮高
	btnSH    = 64  // 小按钮（清空）宽
	btnWRgn  = 96  // 「选择XX区域」按钮宽
	btnWTst  = 88  // 「测试XXOCR」按钮宽
	btnWANm  = 108 // 「只按姓名打招呼」按钮宽
	btnWSel2 = 108 // 「选择求职者姓名」按钮宽
	lblH     = 18  // 标签高
	listH    = 104
	rltH     = 56 // OCR 结果框高
	logH     = 84 // 日志框高
	colX     = 16 // 左边距
	wideW    = 390
	topY     = 12
)

// OCR 任务类型
const (
	ocrKindName = iota + 1
	ocrKindOnline
)

var (
	cfg      Config
	hwndMain HWND

	hwndCapHint   HWND
	hwndCapCount  HWND
	hwndCapList   HWND
	hwndNameRgn   HWND
	hwndOcrStat   HWND
	hwndOcrText   HWND
	hwndOnlRgn    HWND
	hwndOnlStat   HWND
	hwndOnlText   HWND
	hwndDetCur    HWND
	hwndDetCnt    HWND
	hwndLog       HWND
	hwndAutoStat  HWND
	hwndForceStat HWND
)

// ocrState 是一次识别的结果。
type ocrState struct {
	text    string
	elapsed time.Duration
	err     error
	tested  bool
}

// OCR 结果从后台 goroutine 交回主线程。
var (
	ocrMu     sync.Mutex
	ocrName   ocrState
	ocrOnline ocrState
	ocrBusy   bool
)

func wndProc(hwnd HWND, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		createControls(hwnd)
		appendLog("程序启动，配置：%s", configPath())
		if !startHotkey() {
			appendLog("紧急停止热键（Esc）安装失败，请用 [停止] 按钮")
		}
		updateDisplay()
		updateDetectUI()
		return 0

	case WM_COMMAND:
		switch int(uint16(wparam)) {
		case idBtnStart:
			startCaptureFlow()
		case idBtnStop:
			stopCaptureFlow()
		case idBtnClearClks:
			clearClickPoints()
		case idBtnSelName:
			selectOCRRegion(ocrKindName)
		case idBtnTestName:
			runOCRRegion(ocrKindName, cfg.NameRegion)
		case idBtnClearName:
			clearOCRRegion(ocrKindName)
		case idBtnSelOnline:
			selectOCRRegion(ocrKindOnline)
		case idBtnTestOnline:
			runOCRRegion(ocrKindOnline, cfg.OnlineRegion)
		case idBtnClearOnl:
			clearOCRRegion(ocrKindOnline)
		case idBtnStartDet:
			startDetection()
		case idBtnStopDet:
			stopDetection("")
		case idBtnClearLog:
			clearLog()
			updateDetectUI()
		case idBtnAuto:
			startAuto(autoModeOnline)
		case idBtnAutoName:
			startAuto(autoModeNameOnly)
		case idBtnAutoStop:
			stopAuto("")
		case idBtnForce:
			startForceClick()
		case idBtnForceStop:
			stopForceClick("")
		case idBtnExit:
			destroyWindow(hwnd)
		}
		return 0

	// 钩子线程发来的通知：都回到主线程处理
	case WM_APP_CLICK:
		drainClicks()
		return 0

	case WM_APP_STOPCAP:
		// Esc 是紧急停止：采集、检测、自动打招呼全停掉
		stopCaptureFlow()
		stopDetection("按了 Esc")
		stopAuto("按了 Esc")
		stopForceClick("按了 Esc")
		return 0

	// OCR 后台 goroutine 发来的通知（wparam = 任务类型）
	case WM_APP_OCR:
		onOCRReady(int(wparam))
		return 0

	// 检测后台 goroutine 发来的通知：刷新检测状态和日志
	case WM_APP_DETECT:
		updateDetectUI()
		return 0

	case WM_CTLCOLORSTATIC:
		// 让标签背景跟随窗口底色，避免出现白底方块
		return getSysColorBrush(COLOR_BTNFACE)

	case WM_DESTROY:
		stopCapture()
		stopDetection("")
		stopAuto("")
		stopForceClick("")
		postQuitMessage(0)
		return 0
	}
	return defWindowProc(hwnd, msg, wparam, lparam)
}

// layout 记录各控件自上而下的 y 坐标。控件按顺序排，保证互不重叠。
type layout struct {
	btnCap, hintRow, countRow int
	listRow                   int
	btnName, lblName          int
	lblOcr, ocrRow            int
	btnOnl, lblOnl            int
	lblOnlStat, onlRow        int
	btnDet, lblDetCur         int
	lblDetCnt, logRow         int
	btnAuto, lblAuto          int
	btnForce, lblForce        int
	exitRow                   int
}

func controlY() layout {
	var l layout
	y := topY
	l.btnCap = y
	y += btnH + 6
	l.hintRow = y
	y += lblH + 2
	l.countRow = y
	y += lblH + 6
	l.listRow = y
	y += listH + 12

	l.btnName = y
	y += btnH + 2
	l.lblName = y
	y += lblH + 6
	l.lblOcr = y
	y += lblH + 2
	l.ocrRow = y
	y += rltH + 12

	l.btnOnl = y
	y += btnH + 2
	l.lblOnl = y
	y += lblH + 6
	l.lblOnlStat = y
	y += lblH + 2
	l.onlRow = y
	y += rltH + 12

	l.btnDet = y
	y += btnH + 2
	l.lblDetCur = y
	y += lblH + 2
	l.lblDetCnt = y
	y += lblH + 6
	l.logRow = y
	y += logH + 12

	l.btnAuto = y
	y += btnH + 2
	l.lblAuto = y
	y += lblH + 10

	l.btnForce = y
	y += btnH + 2
	l.lblForce = y
	y += lblH + 10

	l.exitRow = y
	return l
}

// clientSize 算出刚好放得下所有控件的客户区大小。
func clientSize() (int, int) {
	return colX + wideW + colX, controlY().exitRow + btnH + topY
}

func createControls(hwnd HWND) {
	hinst := getModuleHandle()
	font := getStockObject(DEFAULT_GUI_FONT)

	mkButton := func(text string, id int, x, y, w, h int) HWND {
		return createWindowEx(0,
			utf16ptr("BUTTON"), utf16ptr(text),
			WS_CHILD|WS_VISIBLE|BS_PUSHBUTTON,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, HMENU(id), hinst, 0)
	}
	mkLabel := func(text string, x, y, w, h int) HWND {
		return createWindowEx(0,
			utf16ptr("STATIC"), utf16ptr(text),
			WS_CHILD|WS_VISIBLE|SS_LEFT,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, 0, hinst, 0)
	}
	mkReadOnly := func(x, y, w, h int) HWND {
		return createWindowEx(WS_EX_CLIENTEDGE,
			utf16ptr("EDIT"), utf16ptr(""),
			WS_CHILD|WS_VISIBLE|WS_VSCROLL|ES_MULTILINE|ES_READONLY|ES_AUTOVSCROLL,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, 0, hinst, 0)
	}

	l := controlY()
	all := []HWND{}

	btnStart := mkButton("开始采集", idBtnStart, colX, l.btnCap, btnW, btnH)
	btnStop := mkButton("停止采集", idBtnStop, colX+btnW+6, l.btnCap, btnW, btnH)
	btnClear := mkButton("清空采集", idBtnClearClks, colX+2*(btnW+6), l.btnCap, btnW, btnH)

	hwndCapHint = mkLabel("", colX, l.hintRow, wideW, lblH)
	hwndCapCount = mkLabel("已采集点击点：0", colX, l.countRow, wideW, lblH)
	hwndCapList = mkReadOnly(colX, l.listRow, wideW, listH)

	btnSelName := mkButton("选择求职者姓名", idBtnSelName, colX, l.btnName, btnWSel2, btnH)
	btnTestName := mkButton("测试OCR", idBtnTestName, colX+btnWSel2+6, l.btnName, btnWTst, btnH)
	btnClrName := mkButton("清空", idBtnClearName, colX+btnWSel2+6+btnWTst+6, l.btnName, btnSH, btnH)
	hwndNameRgn = mkLabel("求职者姓名：未设置", colX, l.lblName, wideW, lblH)
	hwndOcrStat = mkLabel("求职者姓名：未测试", colX, l.lblOcr, wideW, lblH)
	hwndOcrText = mkReadOnly(colX, l.ocrRow, wideW, rltH)

	btnSelOnl := mkButton("选择在线状态", idBtnSelOnline, colX, l.btnOnl, btnWRgn, btnH)
	btnTestOnl := mkButton("测试OCR", idBtnTestOnline, colX+btnWRgn+6, l.btnOnl, btnWTst, btnH)
	btnClrOnl := mkButton("清空", idBtnClearOnl, colX+btnWRgn+6+btnWTst+6, l.btnOnl, btnSH, btnH)
	hwndOnlRgn = mkLabel("在线状态：未设置", colX, l.lblOnl, wideW, lblH)
	hwndOnlStat = mkLabel("在线状态：未测试", colX, l.lblOnlStat, wideW, lblH)
	hwndOnlText = mkReadOnly(colX, l.onlRow, wideW, rltH)

	btnStartDet := mkButton("开始检测", idBtnStartDet, colX, l.btnDet, btnW, btnH)
	btnStopDet := mkButton("停止检测", idBtnStopDet, colX+btnW+6, l.btnDet, btnW, btnH)
	btnClearLog := mkButton("清空日志", idBtnClearLog, colX+2*(btnW+6), l.btnDet, btnW, btnH)
	hwndDetCur = mkLabel("当前人员：未开始", colX, l.lblDetCur, wideW, lblH)
	hwndDetCnt = mkLabel("", colX, l.lblDetCnt, wideW, lblH)
	hwndLog = mkReadOnly(colX, l.logRow, wideW, logH)

	btnAuto := mkButton("开始打招呼", idBtnAuto, colX, l.btnAuto, btnWRgn, btnH)
	btnAutoName := mkButton("只按姓名打招呼", idBtnAutoName, colX+btnWRgn+6, l.btnAuto, btnWANm, btnH)
	btnAutoStop := mkButton("停止", idBtnAutoStop, colX+btnWRgn+6+btnWANm+6, l.btnAuto, btnW, btnH)
	hwndAutoStat = mkLabel("打招呼：未开始", colX, l.lblAuto, wideW, lblH)

	btnForce := mkButton("强制点击", idBtnForce, colX, l.btnForce, btnWRgn, btnH)
	btnForceStop := mkButton("停止强制点击", idBtnForceStop, colX+btnWRgn+6, l.btnForce, btnWRgn, btnH)
	hwndForceStat = mkLabel("强制点击：未开始", colX, l.lblForce, wideW, lblH)

	btnExit := mkButton("退出", idBtnExit, colX, l.exitRow, btnW, btnH)

	all = append(all, btnStart, btnStop, btnClear,
		btnSelName, btnTestName, btnClrName, btnSelOnl, btnTestOnl, btnClrOnl,
		btnStartDet, btnStopDet, btnClearLog, btnAuto, btnAutoName, btnAutoStop,
		btnForce, btnForceStop, btnExit,
		hwndCapHint, hwndCapCount, hwndCapList,
		hwndNameRgn, hwndOcrStat, hwndOcrText,
		hwndOnlRgn, hwndOnlStat, hwndOnlText,
		hwndDetCur, hwndDetCnt, hwndLog, hwndAutoStat, hwndForceStat)

	if font != 0 {
		for _, h := range all {
			sendMessage(h, WM_SETFONT, font, 1)
		}
	}
}

// ---- 点击点采集 ----

// clearClickPoints 清空已采集的点击点（包括还没入队的那些）。
func clearClickPoints() {
	cfg.ClickPoints = []Point{}
	clickMu.Lock()
	clickQueue = nil
	clickMu.Unlock()
	updateDisplay()
	_ = saveConfig(cfg)
}

// startCaptureFlow 启动全局鼠标监听（采集点击点）。
func startCaptureFlow() {
	if capturing {
		return
	}
	if !startCapture() {
		setWindowText(hwndCapHint, utf16ptr("启动失败：全局鼠标监听安装不上"))
		return
	}
	setWindowText(hwndCapHint, utf16ptr("采集中：在 Boss 页面正常点击即可，按 Esc 或 [停止采集] 结束"))
}

// stopCaptureFlow 停止监听，并把队列里剩下的点收干净。
func stopCaptureFlow() {
	if !capturing {
		return
	}
	stopCapture()
	drainClicks()
	setWindowText(hwndCapHint, utf16ptr("已停止采集"))
}

// ---- OCR 区域（姓名 / 在线状态）----

// regionOf 取某个 OCR 任务对应的区域。
func regionOf(kind int) Rect {
	if kind == ocrKindName {
		return cfg.NameRegion
	}
	return cfg.OnlineRegion
}

// setRegion 写回某个 OCR 任务对应的区域。
func setRegion(kind int, r Rect) {
	if kind == ocrKindName {
		cfg.NameRegion = r
	} else {
		cfg.OnlineRegion = r
	}
}

// selectOCRRegion 框选一块 OCR 区域并保存。
func selectOCRRegion(kind int) {
	r, ok := selectRegion(hwndMain)
	showWindow(hwndMain, SW_SHOW)
	setForegroundWindow(hwndMain)
	if !ok {
		return
	}
	setRegion(kind, r)
	appendLog("%s设置完成：%d,%d - %d,%d", kindLabel(kind),
		r.Left, r.Top, r.Right, r.Bottom)
	updateDisplay()
	_ = saveConfig(cfg)
}

// clearOCRRegion 清空某块区域和它的识别结果。
func clearOCRRegion(kind int) {
	setRegion(kind, Rect{})
	ocrMu.Lock()
	if kind == ocrKindName {
		ocrName = ocrState{}
	} else {
		ocrOnline = ocrState{}
	}
	ocrMu.Unlock()
	appendLog("已清空%s", kindLabel(kind))
	updateDisplay()
	_ = saveConfig(cfg)
}

// runOCRRegion 截图 + 识别。截图和识别都放后台 goroutine，完成后 PostMessage 回主线程。
func runOCRRegion(kind int, region Rect) {
	if ocrBusy {
		return
	}
	if !rectIsSet(region) {
		setWindowText(statLabel(kind), utf16ptr("请先框选区域"))
		return
	}

	ocrBusy = true
	setWindowText(statLabel(kind), utf16ptr(kindLabel(kind)+"：识别中…"))

	go func() {
		st := ocrState{tested: true}
		img, err := captureRect(region)
		if err != nil {
			st.err = err
		} else {
			start := time.Now()
			st.text, st.err = recognizeBitmap(img)
			st.elapsed = time.Since(start)
		}

		ocrMu.Lock()
		if kind == ocrKindName {
			ocrName = st
		} else {
			ocrOnline = st
		}
		ocrMu.Unlock()

		postMessage(hwndMain, WM_APP_OCR, uintptr(kind), 0)
	}()
}

// onOCRReady 在主线程把后台识别结果刷到界面上。
func onOCRReady(kind int) {
	ocrBusy = false
	updateOCRDisplay(kind)

	ocrMu.Lock()
	st := ocrName
	if kind == ocrKindOnline {
		st = ocrOnline
	}
	ocrMu.Unlock()

	if st.err != nil {
		appendLog("%s失败：%s", kindLabel(kind), st.err)
	} else if kind == ocrKindOnline {
		appendLog("在线状态 OCR：%q → %s", st.text, onlineVerdict(st.text))
	} else {
		appendLog("求职者姓名 OCR：%q（%dms）", st.text, st.elapsed.Milliseconds())
	}
	updateDetectUI()
}

// ---- 界面刷新 ----

func kindLabel(kind int) string {
	if kind == ocrKindName {
		return "求职者姓名"
	}
	return "在线状态"
}

func statLabel(kind int) HWND {
	if kind == ocrKindName {
		return hwndOcrStat
	}
	return hwndOnlStat
}

func textBox(kind int) HWND {
	if kind == ocrKindName {
		return hwndOcrText
	}
	return hwndOnlText
}

// firstLine 取多行文本的第一行，用于状态栏显示。
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// onlineVerdict 把在线区域的 OCR 文本判成「在线 / 不在线」。
func onlineVerdict(text string) string {
	if strings.TrimSpace(text) == "" {
		return "不在线"
	}
	if looksOnline(text) {
		return "在线"
	}
	return "不在线"
}

// updateOCRDisplay 按任务类型刷新对应的状态标签和结果框。
func updateOCRDisplay(kind int) {
	ocrMu.Lock()
	st := ocrName
	if kind == ocrKindOnline {
		st = ocrOnline
	}
	ocrMu.Unlock()

	stat := statLabel(kind)
	box := textBox(kind)

	if !st.tested {
		if kind == ocrKindName {
			setWindowText(stat, utf16ptr("求职者姓名：未测试"))
		} else {
			setWindowText(stat, utf16ptr("在线状态：未测试"))
		}
		setWindowText(box, utf16ptr(""))
		return
	}

	if st.err != nil {
		setWindowText(stat, utf16ptr(kindLabel(kind)+"：失败（"+st.err.Error()+"）"))
		setWindowText(box, utf16ptr(""))
		return
	}

	if strings.TrimSpace(st.text) == "" {
		msg := fmt.Sprintf("%s：没识别到文字（%dms）", kindLabel(kind), st.elapsed.Milliseconds())
		if kind == ocrKindOnline {
			msg = fmt.Sprintf("在线状态：不在线（没识别到文字，%dms）", st.elapsed.Milliseconds())
		}
		setWindowText(stat, utf16ptr(msg))
		setWindowText(box, utf16ptr(""))
		return
	}

	if kind == ocrKindOnline {
		setWindowText(stat, utf16ptr(fmt.Sprintf("在线状态：%s（%dms）",
			onlineVerdict(st.text), st.elapsed.Milliseconds())))
	} else {
		setWindowText(stat, utf16ptr(fmt.Sprintf("求职者姓名：%s（%dms）",
			firstLine(st.text), st.elapsed.Milliseconds())))
	}
	setWindowText(box, utf16ptr(st.text))
}

// updateDetectUI 刷新「人员变化检测」那部分的标签和日志框。
func updateDetectUI() {
	detMu.Lock()
	running := detRunning
	cur := detCurrent
	pending := detPending
	pendingN := detPendingN
	samples := detSamples
	changes := detChanges
	detMu.Unlock()

	label := "当前人员："
	switch {
	case cur != "":
		label += cur
	case running:
		label += "等待人员…"
	case samples > 0:
		label += "未识别到"
	default:
		label += "未开始"
	}
	if !running && samples > 0 {
		label += "（已停止）"
	}
	if pending != "" {
		label += fmt.Sprintf("　　待确认：%s %d/%d", pending, pendingN, detectConfirm)
	}
	setWindowText(hwndDetCur, utf16ptr(label))

	state := "已停止"
	if running {
		state = "检测中"
	}
	setWindowText(hwndDetCnt, utf16ptr(fmt.Sprintf(
		"累计识别 %d 次 ｜ 确认换人 %d 次 ｜ 轮询 %dms / 连续 %d 次确认 ｜ %s",
		samples, changes, detectInterval.Milliseconds(), detectConfirm, state)))

	setWindowText(hwndLog, utf16ptr(logText()))
	scrollEditToEnd(hwndLog)
	updateAutoUI()
	updateForceUI()
}

func updateDisplay() {

	setWindowText(hwndCapCount, utf16ptr(fmt.Sprintf("已采集点击点：%d", len(cfg.ClickPoints))))
	var b strings.Builder
	for i, p := range cfg.ClickPoints {
		if i > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "%d. (%d,%d)", i+1, p.X, p.Y)
	}
	setWindowText(hwndCapList, utf16ptr(b.String()))

	setWindowText(hwndNameRgn, utf16ptr(describeRect("求职者姓名", cfg.NameRegion)))
	setWindowText(hwndOnlRgn, utf16ptr(describeRect("在线状态", cfg.OnlineRegion)))

	updateOCRDisplay(ocrKindName)
	updateOCRDisplay(ocrKindOnline)
}

func describeRect(name string, r Rect) string {
	if !rectIsSet(r) {
		return name + "：未设置"
	}
	return fmt.Sprintf("%s：left=%d, top=%d, right=%d, bottom=%d  (%d×%d)",
		name, r.Left, r.Top, r.Right, r.Bottom, r.Right-r.Left, r.Bottom-r.Top)
}

func main() {
	setDPIAware()
	cfg = loadConfig()
	ensureConfigFile(cfg)

	cw, ch := clientSize()
	rc := RECT{Left: 0, Top: 0, Right: int32(cw), Bottom: int32(ch)}
	adjustWindowRect(&rc, WS_OVERLAPPEDWINDOW, false)

	hinst := getModuleHandle()
	wc := WNDCLASSEX{
		Size:       uint32(unsafe.Sizeof(WNDCLASSEX{})),
		WndProc:    syscall.NewCallback(wndProc),
		Instance:   uintptr(hinst),
		ClassName:  utf16ptr("BossHelperMainClass"),
		Cursor:     loadCursor(0, IDC_ARROW),
		Background: uintptr(COLOR_BTNFACE + 1),
	}
	registerClassEx(&wc)

	hwndMain = createWindowEx(
		0,
		utf16ptr("BossHelperMainClass"),
		utf16ptr("Boss Helper V0.6"),
		WS_OVERLAPPEDWINDOW,
		100, 80, rc.Right-rc.Left, rc.Bottom-rc.Top,
		0, 0, hinst, 0)
	if hwndMain == 0 {
		return
	}
	showWindow(hwndMain, SW_SHOW)
	updateWindow(hwndMain)

	var msg MSG
	for {
		if getMessage(&msg) == 0 {
			break
		}
		translateMessage(&msg)
		dispatchMessage(&msg)
	}
}
