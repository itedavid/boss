//go:build windows

package main

import (
	"fmt"
	"strconv"
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
	idBtnClearLog   = 1017
	idBtnAuto       = 1018
	idBtnAutoStop   = 1019
	idBtnForce2     = 1023 // 采集区「强制点击打招呼」：对第1组点击点做强制点击（toggle 开关）
	idBtnStart2     = 1024 // 开始采集下一页按钮
	idBtnStop2      = 1025 // 停止采集下一页按钮
	idBtnClearClks2 = 1026 // 清空下一页按钮
	idBtnForce3     = 1027 // 采集区「强制点击下一页」：对第2组点击点做强制点击（toggle 开关）
	idEditMaxGreet  = 1028 // 「打招呼次数上限」输入框
	idBtnTop        = 1029 // 窗口置顶切换按钮（右上角）
)

// 界面尺寸（像素）
const (
	btnW    = 88 // 普通按钮宽
	btnH    = 28 // 按钮高
	btnWRgn = 96 // 「选择XX区域」按钮宽
	lblH    = 18 // 标签高
	editH   = 22 // 单行输入框高
	listH   = 104
	rltH    = 56 // OCR 结果框高
	logH    = 84 // 日志框高
	colX    = 16 // 左边距
	wideW   = 390
	topY    = 12

	colGap    = 16                      // 两列 OCR 区域之间的间距
	ocrColW   = (wideW - colGap) / 2    // 单列宽，约 187
	ocrLeftX  = colX                    // 左列 x（在线状态）
	ocrRightX = colX + ocrColW + colGap // 右列 x（求职者姓名）
)

// OCR 任务类型
const (
	ocrKindName = iota + 1
	ocrKindOnline
)

var (
	cfg      Config
	hwndMain HWND

	hwndCapStat1  HWND
	hwndCapList1  HWND
	hwndCapStat2  HWND
	hwndCapList2  HWND
	hwndNameRgn   HWND
	hwndOcrStat   HWND
	hwndOcrText   HWND
	hwndOnlRgn    HWND
	hwndOnlStat   HWND
	hwndOnlText   HWND
	hwndLog       HWND
	hwndAutoProg  HWND // 「本次已打 a　本次剩余 b」进度行
	hwndAutoStat  HWND
	hwndMaxGreet  HWND // 「打招呼次数上限」输入框
	hwndForceBtn1 HWND
	hwndForceBtn2 HWND
	hwndTopBtn    HWND // 右上角「窗口置顶」切换按钮
	topMostOn     bool // 当前是否置顶（界面与窗口状态同步）
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
	// 窗口回调由 Windows 直接调用，一旦 panic 会穿过 C 边界直接杀进程。
	// 这里兜住它：记录崩溃、吞掉 panic，程序继续跑。
	defer guard("wndProc")
	switch msg {
	case WM_CREATE:
		createControls(hwnd)
		appendLog("程序启动，配置：%s", configPath())
		if !startHotkey() {
			appendLog("全局热键安装失败，请用界面上的 [停止] 按钮")
		} else {
			appendLog("全局热键：Esc=全部停止，Ctrl+C=只停自动化")
		}
		loadMaxGreetsToUI()
		// 置顶：按配置初始化（默认开启，见 config.go loadConfig）
		topMostOn = cfg.TopMost
		applyTopMost()
		updateDisplay()
		updateDetectUI()
		return 0

	case WM_COMMAND:
		switch int(uint16(wparam)) {
		case idBtnStart:
			startCaptureFlow(1)
		case idBtnStop:
			stopCaptureFlow(1)
		case idBtnClearClks:
			clearClickPoints(1)
		case idBtnStart2:
			startCaptureFlow(2)
		case idBtnStop2:
			stopCaptureFlow(2)
		case idBtnClearClks2:
			clearClickPoints(2)
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
		case idBtnClearLog:
			clearLog()
			updateDetectUI()
		case idBtnAuto:
			syncMaxGreetsFromUI()
			startAuto()
		case idEditMaxGreet:
			// 次数上限改动即时生效（运行中改也能拦住）
			if uint16(wparam>>16) == EN_CHANGE {
				syncMaxGreetsFromUI()
			}
		case idBtnAutoStop:
			stopAuto("")
		case idBtnForce2:
			toggleForceClick(1)
		case idBtnForce3:
			toggleForceClick(2)
		case idBtnExit:
			destroyWindow(hwnd)
		case idBtnTop:
			toggleTopMost()
		}
		return 0

	// 钩子线程发来的通知：都回到主线程处理
	case WM_APP_CLICK:
		// wparam 是点击所属组号（1/2），据此把点并入对应组
		if g := int(wparam); g != 0 {
			drainClicks(g)
		}
		return 0

	case WM_APP_STOPCAP:
		// Esc 是紧急停止：采集、自动打招呼、强制点击全停掉（采集停当前激活组）
		stopCaptureFlow(0)
		stopAuto("按了 Esc")
		stopForceClick(0, "按了 Esc")
		return 0

	case WM_APP_STOPAUTO:
		// Ctrl+C：只打断自动化——自动打招呼 + 强制点击（强制翻页），不碰采集
		stopAuto("按了 Ctrl+C")
		stopForceClick(0, "按了 Ctrl+C")
		return 0

	// OCR 后台 goroutine 发来的通知（wparam = 任务类型）
	case WM_APP_OCR:
		onOCRReady(int(wparam))
		return 0

	// 自动打招呼 / 强制点击等后台 goroutine 发来的刷新通知。
	// 先认领（清标记）再刷新：这样刷新期间新攒下的请求还能再发一条消息进来，
	// 队列里始终最多一条，不会堆积。
	case WM_APP_DETECT:
		takeDetectUI()
		updateDetectUI()
		return 0

	case WM_CTLCOLORSTATIC:
		// 让标签背景跟随窗口底色，避免出现白底方块
		return getSysColorBrush(COLOR_BTNFACE)

	case WM_DESTROY:
		stopCapture()
		stopAuto("")
		stopForceClick(0, "")
		postQuitMessage(0)
		return 0
	}
	return defWindowProc(hwnd, msg, wparam, lparam)
}

// layout 记录各控件自上而下的 y 坐标。控件按顺序排，保证互不重叠。
type layout struct {
	capTop, capBoxRow                                 int
	capBtnStart, capBtnStop, capBtnClear, capBtnForce int
	capBottom                                         int

	// 两个 OCR 区域并排：左=在线状态，右=求职者姓名（两列共用同一套 y）
	ocrTop, ocrBoxRow                  int
	ocrBtnSel, ocrBtnTest, ocrBtnClear int
	ocrStatRow, ocrBottom              int

	btnLog, logRow       int
	btnAuto, lblAutoProg int
	lblAuto, exitRow     int
	topBtnRow            int // 顶部「窗口置顶」按钮所在行
}

func controlY() layout {
	var l layout
	y := topY
	// 顶部「窗口置顶」按钮独占一行（靠右），其余控件在其下方
	l.topBtnRow = y
	y += btnH + 4
	// 采集区：打招呼按钮 / 下一页按钮 两列并排
	l.capTop = y
	y += lblH + 4
	l.capBoxRow = y
	y += listH + 4
	l.capBtnStart = y
	y += btnH + 4
	l.capBtnStop = y
	y += btnH + 4
	l.capBtnClear = y
	y += btnH + 4
	l.capBtnForce = y
	y += btnH + 4
	l.capBottom = y

	// 两个 OCR 区域并排（左右两列共用同一套 y）
	l.ocrTop = y
	y += lblH + 4
	l.ocrBoxRow = y
	y += rltH + 4
	l.ocrBtnSel = y
	y += btnH + 4
	l.ocrBtnTest = y
	y += btnH + 4
	l.ocrBtnClear = y
	y += btnH + 4
	l.ocrStatRow = y
	y += lblH + 12
	l.ocrBottom = y

	l.btnLog = y
	y += btnH + 6
	l.logRow = y
	y += logH + 12

	// 「次数上限」输入框和「开始打招呼」「停止」共用这一行（控件见 createControls）
	l.btnAuto = y
	y += btnH + 2
	// 进度单独占一行（本次已打 / 本次剩余），比塞在状态标签里更好认
	l.lblAutoProg = y
	y += lblH + 2
	l.lblAuto = y
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
	// 单行数字输入框：只收数字、靠右显示
	mkNumEdit := func(x, y, w, h int) HWND {
		return createWindowEx(WS_EX_CLIENTEDGE,
			utf16ptr("EDIT"), utf16ptr(""),
			WS_CHILD|WS_VISIBLE|ES_AUTOHSCROLL|ES_RIGHT|ES_NUMBER,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, HMENU(idEditMaxGreet), hinst, 0)
	}

	l := controlY()
	all := []HWND{}

	// 采集区两列：左=打招呼按钮，右=下一页按钮
	hwndCapStat1 = mkLabel("未采集", ocrLeftX, l.capTop, ocrColW, lblH)
	hwndCapList1 = mkReadOnly(ocrLeftX, l.capBoxRow, ocrColW, listH)
	btnStart := mkButton("开始采集打招呼按钮", idBtnStart, ocrLeftX, l.capBtnStart, ocrColW, btnH)
	btnStop := mkButton("停止采集打招呼按钮", idBtnStop, ocrLeftX, l.capBtnStop, ocrColW, btnH)
	btnClear := mkButton("清空打招呼按钮", idBtnClearClks, ocrLeftX, l.capBtnClear, ocrColW, btnH)
	hwndForceBtn1 = mkButton("强制点击打招呼", idBtnForce2, ocrLeftX, l.capBtnForce, ocrColW, btnH)

	hwndCapStat2 = mkLabel("未采集", ocrRightX, l.capTop, ocrColW, lblH)
	hwndCapList2 = mkReadOnly(ocrRightX, l.capBoxRow, ocrColW, listH)
	btnStart2 := mkButton("开始采集下一页按钮", idBtnStart2, ocrRightX, l.capBtnStart, ocrColW, btnH)
	btnStop2 := mkButton("停止采集下一页按钮", idBtnStop2, ocrRightX, l.capBtnStop, ocrColW, btnH)
	btnClear2 := mkButton("清空下一页按钮", idBtnClearClks2, ocrRightX, l.capBtnClear, ocrColW, btnH)
	hwndForceBtn2 = mkButton("强制点击下一页", idBtnForce3, ocrRightX, l.capBtnForce, ocrColW, btnH)

	// 左列：在线状态
	hwndOnlRgn = mkLabel("在线状态：未设置", ocrLeftX, l.ocrTop, ocrColW, lblH)
	hwndOnlText = mkReadOnly(ocrLeftX, l.ocrBoxRow, ocrColW, rltH)
	btnSelOnl := mkButton("选择在线状态", idBtnSelOnline, ocrLeftX, l.ocrBtnSel, ocrColW, btnH)
	btnTestOnl := mkButton("测试OCR", idBtnTestOnline, ocrLeftX, l.ocrBtnTest, ocrColW, btnH)
	btnClrOnl := mkButton("清空", idBtnClearOnl, ocrLeftX, l.ocrBtnClear, ocrColW, btnH)
	hwndOnlStat = mkLabel("在线状态：未测试", ocrLeftX, l.ocrStatRow, ocrColW, lblH)

	// 右列：求职者姓名
	hwndNameRgn = mkLabel("求职者姓名：未设置", ocrRightX, l.ocrTop, ocrColW, lblH)
	hwndOcrText = mkReadOnly(ocrRightX, l.ocrBoxRow, ocrColW, rltH)
	btnSelName := mkButton("选择求职者姓名", idBtnSelName, ocrRightX, l.ocrBtnSel, ocrColW, btnH)
	btnTestName := mkButton("测试OCR", idBtnTestName, ocrRightX, l.ocrBtnTest, ocrColW, btnH)
	btnClrName := mkButton("清空", idBtnClearName, ocrRightX, l.ocrBtnClear, ocrColW, btnH)
	hwndOcrStat = mkLabel("求职者姓名：未测试", ocrRightX, l.ocrStatRow, ocrColW, lblH)

	btnClearLog := mkButton("清空日志", idBtnClearLog, colX, l.btnLog, btnW, btnH)
	hwndLog = mkReadOnly(colX, l.logRow, wideW, logH)

	// 打招呼次数上限：和「开始打招呼」同一行，放在按钮左边
	lblMaxTitle := mkLabel("次数上限", colX, l.btnAuto+5, 60, lblH)
	hwndMaxGreet = mkNumEdit(colX+64, l.btnAuto+3, 46, editH)
	sendMessage(hwndMaxGreet, EM_SETLIMITTEXT, 6, 0)
	lblMaxHint := mkLabel("0=不限", colX+312, l.btnAuto+5, wideW-312, lblH)

	btnAuto := mkButton("开始打招呼", idBtnAuto, colX+116, l.btnAuto, btnWRgn, btnH)
	btnAutoStop := mkButton("停止", idBtnAutoStop, colX+116+btnWRgn+6, l.btnAuto, btnW, btnH)
	hwndAutoProg = mkLabel("本次已打 0　本次剩余 不限", colX, l.lblAutoProg, wideW, lblH)
	hwndAutoStat = mkLabel("打招呼：未开始", colX, l.lblAuto, wideW, lblH)

	btnExit := mkButton("退出", idBtnExit, colX, l.exitRow, btnW, btnH)

	// 顶部右上角「窗口置顶」切换按钮（标题在「置顶」↔「已置顶·点此取消」间切换）
	btnTop := mkButton("置顶", idBtnTop, colX+wideW-140, l.topBtnRow, 140, btnH)

	all = append(all, btnStart, btnStop, btnClear, hwndForceBtn1,
		btnTop,
		btnStart2, btnStop2, btnClear2, hwndForceBtn2,
		btnSelName, btnTestName, btnClrName, btnSelOnl, btnTestOnl, btnClrOnl,
		btnClearLog, btnAuto, btnAutoStop,
		btnExit,
		hwndCapStat1, hwndCapList1, hwndCapStat2, hwndCapList2,
		hwndNameRgn, hwndOcrStat, hwndOcrText,
		hwndOnlRgn, hwndOnlStat, hwndOnlText,
		hwndLog, hwndAutoStat, hwndAutoProg,
		lblMaxTitle, hwndMaxGreet, lblMaxHint)

	if font != 0 {
		for _, h := range all {
			sendMessage(h, WM_SETFONT, font, 1)
		}
	}
}

// ---- 打招呼次数上限（输入框 ↔ 配置 ↔ 后台循环）----

// parseEditInt 读输入框里的非负整数；空着或读不出数字都按 0（不限）处理。
func parseEditInt(hwnd HWND) int {
	if hwnd == 0 {
		return 0
	}
	buf := make([]uint16, 24)
	n := getWindowText(hwnd, &buf[0], len(buf))
	if n <= 0 {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(syscall.UTF16ToString(buf[:n])))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// loadMaxGreetsToUI 启动时把配置里的次数上限填进输入框。
func loadMaxGreetsToUI() {
	if cfg.MaxGreets > 0 {
		setWindowText(hwndMaxGreet, utf16ptr(strconv.Itoa(cfg.MaxGreets)))
	}
	setAutoMaxGreets(cfg.MaxGreets)
}

// syncMaxGreetsFromUI 把输入框里的次数上限同步到内存配置并落盘。
// 改动即时生效：后台循环每次要打招呼前都会重新读一次，运行中调小也能立刻拦住。
func syncMaxGreetsFromUI() {
	n := parseEditInt(hwndMaxGreet)
	setAutoMaxGreets(n)
	if n != cfg.MaxGreets {
		cfg.MaxGreets = n
		_ = saveConfig(cfg)
	}
	requestDetectUI() // 让「已打招呼 X / 上限 Y」跟着刷新
}

// ---- 窗口置顶 ----

// applyTopMost 把 topMostOn 的当前值同步到窗口层级与按钮标题。
func applyTopMost() {
	if hwndMain == 0 {
		return
	}
	after := hwndNoTopMost
	caption := "置顶"
	if topMostOn {
		after = hwndTopMost
		caption = "已置顶·点此取消"
	}
	setWindowPos(hwndMain, after, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
	if hwndTopBtn != 0 {
		setWindowText(hwndTopBtn, utf16ptr(caption))
	}
}

// toggleTopMost 点击右上角按钮时切换置顶，并写入配置（下次启动保持）。
func toggleTopMost() {
	topMostOn = !topMostOn
	applyTopMost()
	cfg.TopMost = topMostOn
	_ = saveConfig(cfg)
}

// ---- 点击点采集（支持两组，各自独立）----

// groupStat / groupList 取某一组的状态标签 / 点列表控件。
func groupStat(g int) HWND {
	if g == 1 {
		return hwndCapStat1
	}
	return hwndCapStat2
}
func groupList(g int) HWND {
	if g == 1 {
		return hwndCapList1
	}
	return hwndCapList2
}

// groupPoints 把某一组的点击点拼成多行文本。
func groupPoints(g int) string {
	var b strings.Builder
	pts := cfg.ClickPoints
	if g == 2 {
		pts = cfg.ClickPoints2
	}
	for i, p := range pts {
		if i > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "%d. (%d,%d)", i+1, p.X, p.Y)
	}
	return b.String()
}

// groupName 返回某一组对应的功能名（组1=打招呼按钮，组2=下一页按钮）。
func groupName(g int) string {
	if g == 2 {
		return "下一页按钮"
	}
	return "打招呼按钮"
}

// capStatusText 某一组的简短状态文案（采集中由 startCaptureFlow 单独设置，这里不覆盖）。
func capStatusText(g int) string {
	pts := cfg.ClickPoints
	if g == 2 {
		pts = cfg.ClickPoints2
	}
	if len(pts) == 0 {
		return "未采集"
	}
	return fmt.Sprintf("已采集 %d 点", len(pts))
}

// setGroupStatus 设置某一组的状态标签。
func setGroupStatus(g int, text string) {
	setWindowText(groupStat(g), utf16ptr(text))
}

// clearClickPoints 清空某一组已采集的点击点（包括还没入队的那些）。
func clearClickPoints(g int) {
	if g == 1 {
		cfg.ClickPoints = []Point{}
	} else if g == 2 {
		cfg.ClickPoints2 = []Point{}
	}
	clickMu.Lock()
	if g >= 1 && g <= 2 {
		clickQueues[g-1] = nil
	}
	clickMu.Unlock()
	updateDisplay()
	_ = saveConfig(cfg)
}

// startCaptureFlow 启动某一组的全局鼠标监听（采集点击点）。
// 若另一组正在采集，会先收尾那一组再切换，因为全局钩子同一时刻只能服务一组。
func startCaptureFlow(g int) {
	if capturing && captureGroup == g {
		return
	}
	if capturing {
		drainClicks(captureGroup) // 先保存当前组的点
		stopCapture()
	}
	// 强制点击会动鼠标，开始采集前先把强制点击停掉，避免抢鼠标
	stopForceClick(0, "开始采集")
	if !startCapture(g) {
		setGroupStatus(g, "启动失败：全局鼠标监听安装不上")
		return
	}
	setGroupStatus(g, fmt.Sprintf("采集中：在页面点击%s的位置，按 Esc 或 [停止采集%s] 结束", groupName(g), groupName(g)))
}

// stopCaptureFlow 停止某一组的监听，并把队列里剩下的点收干净。
// g 传 0 表示停止「当前正在采集的组」（Esc 紧急停止用）。
func stopCaptureFlow(g int) {
	if g == 0 {
		if !capturing {
			return
		}
		g = captureGroup
	}
	if !capturing || captureGroup != g {
		return
	}
	stopCapture()
	drainClicks(g)
	// 停止时删掉最后一条：剔除「最小化后点任务栏恢复窗口」那一下误采的点
	// （它落在任务栏上，不在本窗口矩形内，现有过滤拦不到，正好是停止前的最后一条）。
	dropped := dropLastClickPoint(g)
	if dropped {
		setGroupStatus(g, "已停止采集（已移除最后一条误采点）")
		appendLog("%s 已停止采集，并移除最后一条采集点（恢复窗口时的点击）", groupName(g))
	} else {
		setGroupStatus(g, "已停止采集")
	}
}

// dropLastClickPoint 删除某一组最近采集的一条点，返回是否真的删了。
// 用途：停止采集时剔掉「恢复窗口」那一下落在任务栏上的误采点。
func dropLastClickPoint(g int) bool {
	if g < 1 || g > 2 {
		return false
	}
	var pts *[]Point
	if g == 1 {
		pts = &cfg.ClickPoints
	} else {
		pts = &cfg.ClickPoints2
	}
	if len(*pts) == 0 {
		return false
	}
	*pts = (*pts)[:len(*pts)-1]
	updateDisplay()
	_ = saveConfig(cfg)
	return true
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

	safeGo("runOCRRegion", func() {
		// 万一这个 goroutine 崩了，也要把 ocrBusy 放开，否则「测试OCR」按钮会一直点不动。
		defer func() {
			if r := recover(); r != nil {
				ocrBusy = false
				recordCrash("runOCRRegion", r)
			}
		}()
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
	})
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

// updateDetectUI 刷新日志框和「自动打招呼 / 强制点击」的状态显示。
//
// 名字里的 Detect 是历史原因（它由 WM_APP_DETECT 消息驱动），独立的「人员变化检测」
// 功能已经删除，现在它只负责：日志框 + 自动打招呼两行 + 强制点击按钮文案。
func updateDetectUI() {
	setWindowText(hwndLog, utf16ptr(logText()))
	scrollEditToEnd(hwndLog)
	updateAutoUI()
	updateForceUI()
}

func updateDisplay() {

	// 采集区两组：采集中时保留各自的「采集中…」文案，只刷新点列表
	for _, g := range []int{1, 2} {
		if !(capturing && captureGroup == g) {
			setWindowText(groupStat(g), utf16ptr(capStatusText(g)))
		}
		setWindowText(groupList(g), utf16ptr(groupPoints(g)))
	}

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
	// 兜住主 goroutine 的 panic：崩溃时留下 crash.log 而不是无声消失。
	defer guard("main")
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
		utf16ptr("Boss Helper V0.8"),
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
