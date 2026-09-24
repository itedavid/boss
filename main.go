//go:build windows

package main

import (
	"runtime"
	"syscall"
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
	idBtnPage       = 1030 // 页面切换按钮（打招呼页 ↔ 快捷回复页）
	idBtnQkRun      = 1031 // 「开始点击 / 停止点击」：轮流点击 B 页 6 组已采集的坐标点
)

// 「快捷回复」页 6 组控件的按钮 ID。
// 每组三个按钮各占连续的 quickGroupCount 个号（quickGroupCount 见 config.go）。
// 派发时用区间判断是哪一类、减基数得到组号。
const (
	qkBtnStartBase = 3000
	qkBtnStopBase  = qkBtnStartBase + quickGroupCount
	qkBtnClearBase = qkBtnStopBase + quickGroupCount
	qkBtnRangeEnd  = qkBtnClearBase + quickGroupCount
)

// 「快捷回复」页 6 组「点击间隔」输入框的 ID。
// 每组的输入框只占 1 个号，所以起点要在上面那三段按钮号之后再留一段安全距离，
// 免得以后给按钮加段时撞号。改动即时生效（EN_CHANGE，见 wndProc）。
const qkEditGapBase = 4000

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

	// 快捷回复页：每行 qkCols 组，每组一块（标签 + 列表 + 按钮行）
	qkCols  = 2                    // 每行排几组
	qkColW  = (wideW - colGap) / 2 // 单组宽，与 OCR 单列同宽
	qkListH = 72                   // 点列表高度（比采集区的矮些，好放下 3 行）
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

	hwndPageBtn HWND // 「打招呼 / 快捷回复」页面切换按钮

	// 快捷回复页（B 页）的控件：每组一套，下标 0..quickGroupCount-1。
	hwndQkStat  [quickGroupCount]HWND // 该组状态标签
	hwndQkList  [quickGroupCount]HWND // 该组点列表
	hwndQkStart [quickGroupCount]HWND // 开始采集
	hwndQkStop  [quickGroupCount]HWND // 停止采集
	hwndQkClear [quickGroupCount]HWND // 清空
	hwndQkGap   [quickGroupCount]HWND // 点击间隔输入框（毫秒）

	hwndQkRunBtn  HWND // B 页「开始点击 / 停止点击」（公共区，不属于某一组）
	hwndQkRunStat HWND // B 页轮流点击的状态说明行

	// A 页（打招呼页）的控件集合，切页时整批显隐用。
	pageACtrls []HWND
	// B 页（快捷回复页）的控件集合。
	pageBCtrls []HWND
	// 当前页：false=打招呼页，true=快捷回复页。
	quickPage bool
)

func wndProc(hwnd HWND, msg uint32, wparam, lparam uintptr) uintptr {
	// 窗口回调由 Windows 直接调用，一旦 panic 会穿过 C 边界直接杀进程。
	// 这里兜住它：记录崩溃、吞掉 panic，程序继续跑。
	defer guard("wndProc")
	switch msg {
	case WM_CREATE:
		// 注意：createWindowEx 是在 WM_CREATE 处理完之后才返回并赋值给 hwndMain 的，
		// 所以此刻包级的 hwndMain 还是 0——必须用回调参数 hwnd（这才是真窗口句柄）。
		// 之前「启动回填不生效」就是因为往 0 投消息，postMessage 直接失败。
		hwndMain = hwnd
		createControls(hwnd)
		appendLog("程序启动，配置：%s", configPath())
		if !startHotkey() {
			appendLog("全局热键安装失败，请用界面上的 [停止] 按钮")
		} else {
			appendLog("全局热键：Esc=全部停止，Ctrl+C=只停自动化")
		}
		// 配置回填不能在 WM_CREATE 里直接做：此时窗口还没创建完，
		// SetWindowText 写进去的文本会在创建流程收尾时被系统重置掉，输入框最终仍是空的。
		// 所以只投一条消息给自己，等 WM_CREATE 返回、窗口真正就绪后再回填（见 WM_APP_LOADCFG）。
		postMessage(hwnd, WM_APP_LOADCFG, 0, 0)
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
		case idBtnPage:
			togglePage()
		case idBtnQkRun:
			toggleQuickClick()
		default:
			// 快捷回复页 6 组的 开始/停止/清空：ID 按「类型 + 组号」连续排（见 qkBtnStartBase）
			// 以及 6 个「点击间隔」输入框（见 qkEditGapBase）。
			dispatchQuickControl(int(uint16(wparam)), uint16(wparam>>16))
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
		// Esc 是紧急停止：采集、自动打招呼、强制点击、轮流点击全停掉（采集停当前激活组）
		stopCaptureFlow(0)
		stopAuto("按了 Esc")
		stopForceClick(0, "按了 Esc")
		stopQuickClick("按了 Esc")
		return 0

	case WM_APP_STOPAUTO:
		// Ctrl+C：只打断自动化——自动打招呼 + 强制点击 + 轮流点击，不碰采集
		stopAuto("按了 Ctrl+C")
		stopForceClick(0, "按了 Ctrl+C")
		stopQuickClick("按了 Ctrl+C")
		return 0

	// 窗口创建完成后，把配置里的值回填到各个输入框。
	// 必须是「创建完之后」：WM_CREATE 期间 SetWindowText 的文本会被系统重置。
	case WM_APP_LOADCFG:
		loadMaxGreetsToUI()
		loadQuickIntervalsToUI()
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
		stopQuickClick("")
		postQuitMessage(0)
		return 0
	}
	return defWindowProc(hwnd, msg, wparam, lparam)
}

func main() {

	// 兜住主 goroutine 的 panic：崩溃时留下 crash.log 而不是无声消失。
	defer guard("main")

	// 把 main goroutine 钉死在当前 OS 线程上，整个进程期间不解锁。
	//
	// 这是 Win32 的硬性要求：窗口归属于「创建它的那条 OS 线程」，消息队列也是线程级的，
	// 只有那条线程调 GetMessage 才能取到本窗口的消息。而 Go 的 goroutine 默认不绑定线程，
	// 调度器会在阻塞点（channel 等待、系统调用、GC 等）把它挪到别的线程上继续跑。
	//
	// 一旦 main goroutine 在 createWindowEx 之后被挪走，后面的 getMessage 就跑在另一条
	// 线程上、抽的是那条线程的空队列，真正的窗口队列再也没人取——界面就永久卡死：
	// 鼠标移到窗口上转圈、点不动，窗口外一切正常，且不会自行恢复。
	// 本程序在 WM_CREATE→startHotkey() 和「开始采集」→startCapture() 里都会 <-ready 阻塞，
	// 正是最容易触发迁移的两个点，对应「刚打开」和「点采集按钮」时偶发的卡死。
	runtime.LockOSThread()

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
