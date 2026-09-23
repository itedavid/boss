//go:build windows

// auto.go 负责「自动打招呼」主循环：
//
//	识别姓名
//	  换人了（名字与上一人不同）-> 检查在线
//	      在线   -> 点[打招呼按钮]（组1）打招呼，随机停 0~0.5 秒
//	      不在线 -> 什么都不点
//	  没换人（同名 / 读不出字）-> 不打招呼
//	-> 两种情况都点[下一页按钮]（组2）翻到下一个，随机停留 0.8~1.5 秒
//	-> 回到开头，重复
//
// 每轮必定以「翻页」结束，循环里没有「什么都不做」的分支，所以结构上不可能卡住。
// 翻页本身带 0.8~1.5 秒停留，页面有足够时间渲染，不会连着读到加载中的旧页面。
//
// 整个循环跑在后台 goroutine 上，不阻塞界面；所有界面刷新都靠 PostMessage 回主线程。
// 停止是「硬停止」：点完 [停止] 或按 Esc 之后，既不再截图识别，也不会再点鼠标。
package main

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// 状态机的状态（只影响显示和日志，逻辑都在 autoLoop 里）
const (
	stWaiting  = "WAITING"
	stReadP    = "READ_PERSON"
	stChanged  = "PERSON_CHANGED"
	stCheckOnl = "CHECK_ONLINE"
	stOnline   = "ONLINE"
	stGreeting = "GREETING"
	stWaitNext = "WAIT_NEXT_PERSON"
	stStopped  = "STOPPED"
)

const (
	clickJitterPx = 4 // 点击位置在配置的点附近随机偏 ±4 像素

	// 主循环节奏：每轮「截图 + 识别」之间歇多久（也用于截图/识别失败后的重试间隔）
	autoInterval = 500 * time.Millisecond

	// 打完招呼后的短停留：随机 0~greetPauseMax，然后直接翻页下一个用户。
	// 按用户要求「隔 0.5 秒以内的随机数，直接就去翻页」。
	greetPauseMax = 500 * time.Millisecond

	// 点完「下一页按钮」后的随机停留：0.8~1.5 秒，防止人机检测
	nextPauseMin = 800 * time.Millisecond
	nextPauseMax = 1500 * time.Millisecond
)

// jitterPoint 在配置的点击点附近随机偏一点，避免每次都落在同一个像素上。
// n 是最大偏移量（像素），点还是在原来的按钮里。
func jitterPoint(p Point, n int) Point {
	if n <= 0 {
		return p
	}
	return Point{X: p.X + rand.Intn(2*n+1) - n, Y: p.Y + rand.Intn(2*n+1) - n}
}

var (
	autoMu      sync.Mutex
	autoRunning bool
	autoStopCh  chan struct{}
	autoState   string // "" 表示还没启动过
	autoPerson  string // 最近一次确认的人员
	autoGreets  int    // 成功打招呼次数
	autoLastPt  int    // 上一次用的点击点下标（下一次尽量换一个）
	autoLastErr string // 上一次的错误信息，避免刷屏
)

// autoMaxGreets 是本轮「最多打多少次招呼」，0 = 不限。
//
// 用原子变量而不是普通 int：界面上改一次就写一次，后台循环每次要打招呼前都读一次，
// 所以运行中途调大 / 调小都能立刻生效，也不用加锁。
var autoMaxGreets int32

// setAutoMaxGreets 设置打招呼次数上限（负数按 0 = 不限处理）。
func setAutoMaxGreets(n int) {
	if n < 0 {
		n = 0
	}
	atomic.StoreInt32(&autoMaxGreets, int32(n))
}

// autoMaxGreetsNow 读当前的打招呼次数上限，0 表示不限。
func autoMaxGreetsNow() int {
	return int(atomic.LoadInt32(&autoMaxGreets))
}

// autoGreetLimitReached 判断是否已经打到设定的次数上限。
func autoGreetLimitReached() bool {
	m := autoMaxGreetsNow()
	if m <= 0 {
		return false
	}
	autoMu.Lock()
	n := autoGreets
	autoMu.Unlock()
	return n >= m
}

// greetProgressText 把本次进度拼成一句「本次已打 a　本次剩余 b」。
// 没设上限（0）时剩余显示「不限」；上限被中途调小到小于已打数时剩余按 0 算，不出现负数。
// 界面上的进度行和日志里的起始行共用它，保证两处措辞一致。
func greetProgressText(greeted int) string {
	m := autoMaxGreetsNow()
	if m <= 0 {
		return fmt.Sprintf("本次已打 %d　本次剩余 不限", greeted)
	}
	rest := m - greeted
	if rest < 0 {
		rest = 0
	}
	return fmt.Sprintf("本次已打 %d　本次剩余 %d", greeted, rest)
}

// 下面三个变量是「可替换的实现」：正常运行时就是真实的截图 / 识别 / 点击，
// 单元测试里换成假实现，就能在不动鼠标、不截屏的情况下验证整条流程。
var (
	autoCapture   = captureRect
	autoRecognize = recognizeBitmap
	autoClick     = clickAt
)

// autoStateText 把状态码翻成中文，给界面用。
func autoStateText(s string) string {
	switch s {
	case stWaiting:
		return "等待人员"
	case stReadP:
		return "识别人员"
	case stChanged:
		return "检测到新人员"
	case stCheckOnl:
		return "检查在线状态"
	case stOnline:
		return "在线"
	case stGreeting:
		return "正在打招呼"
	case stWaitNext:
		return "等待下一人员"
	case stStopped:
		return "已停止"
	}
	return "未开始"
}

func setAutoState(s string) {
	autoMu.Lock()
	autoState = s
	autoMu.Unlock()
	requestDetectUI()
}

func setAutoPerson(name string) {
	autoMu.Lock()
	autoPerson = name
	autoMu.Unlock()
}

// startAuto 开始自动打招呼。开始前先把该有的配置都检查一遍。
func startAuto() {
	autoMu.Lock()
	if autoRunning {
		autoMu.Unlock()
		return
	}
	autoMu.Unlock()

	if !rectIsSet(cfg.NameRegion) {
		appendLog("开始打招呼失败：请先框选求职者姓名区域")
		requestDetectUI()
		return
	}
	if !rectIsSet(cfg.OnlineRegion) {
		appendLog("开始打招呼失败：请先框选在线状态区域")
		requestDetectUI()
		return
	}
	if len(cfg.ClickPoints) == 0 {
		appendLog("开始打招呼失败：请先在「开始采集打招呼按钮」采集至少一个点")
		requestDetectUI()
		return
	}
	if len(cfg.ClickPoints2) == 0 {
		appendLog("开始打招呼失败：请先在「开始采集下一页按钮」采集至少一个点")
		requestDetectUI()
		return
	}

	// 强制点击会和打招呼抢鼠标，先停掉
	stopForceClick(0, "自动打招呼开始")

	stop := make(chan struct{})
	autoMu.Lock()
	autoRunning = true
	autoStopCh = stop
	autoState = stWaiting
	autoPerson = ""
	autoGreets = 0
	autoLastErr = ""
	autoMu.Unlock()

	appendLog("开始打招呼：求职者姓名区域 %d,%d - %d,%d；在线状态区域 %d,%d - %d,%d；打招呼点 %d 个／下一页点 %d 个",
		cfg.NameRegion.Left, cfg.NameRegion.Top, cfg.NameRegion.Right, cfg.NameRegion.Bottom,
		cfg.OnlineRegion.Left, cfg.OnlineRegion.Top, cfg.OnlineRegion.Right, cfg.OnlineRegion.Bottom,
		len(cfg.ClickPoints), len(cfg.ClickPoints2))
	if autoMaxGreetsNow() > 0 {
		appendLog("%s（打满自动停止）", greetProgressText(0))
	} else {
		appendLog("%s", greetProgressText(0))
	}
	setAutoState(stWaiting)

	// 配置在启动时快照一份给后台用，避免和主线程写配置打架
	nameRegion := cfg.NameRegion
	onlineRegion := cfg.OnlineRegion
	greetPoints := append([]Point(nil), cfg.ClickPoints...) // 组1：打招呼按钮
	nextPoints := append([]Point(nil), cfg.ClickPoints2...) // 组2：下一页按钮

	safeGo("autoLoop", func() {
		autoLoop(nameRegion, onlineRegion, greetPoints, nextPoints, stop)
	})
}

// stopAuto 停止自动打招呼。reason 会记进日志，方便看出是谁停的。
func stopAuto(reason string) {
	autoMu.Lock()
	if !autoRunning {
		autoMu.Unlock()
		return
	}
	autoRunning = false
	close(autoStopCh)
	autoStopCh = nil
	autoState = stStopped
	autoMu.Unlock()

	if reason != "" {
		appendLog("停止打招呼（%s）", reason)
	} else {
		appendLog("停止打招呼")
	}
	requestDetectUI()
}

// autoStopped 检查停止信号。点击前必须再查一次，保证「停了就不会再点」。
func autoStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// sleepOrStop 等待一段时间；返回 true 表示期间收到了停止信号。
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return true
	case <-t.C:
		return false
	}
}

// autoLoop 是自动打招呼主循环。
// greetPoints 是组1（打招呼按钮），nextPoints 是组2（下一页按钮）。
//
// 判定只看一件事：屏幕上是不是换人了。
//
//	读到与 current 不同的新名字 -> 新人：查在线 -> 在线就打招呼
//	读到同名 / 读不出字         -> 上一次翻页没生效，这一轮不打招呼
//
// 关键是这两种情况最后**都要翻页**，循环里没有任何「什么都不做」的分支，
// 所以结构上不可能卡住：
//   - 页面没变   -> 读到同名 -> 重翻一次
//   - OCR 抖动   -> 每轮读出的都不一样 -> 当成新人正常处理
//   - 读不出字   -> 重翻一次
//
// 翻页本身带 0.8~1.5 秒停留（见 clickNextPage），页面有足够时间渲染完，
// 不会连着读到「加载中」的旧页面。
func autoLoop(nameRegion, onlineRegion Rect, greetPoints, nextPoints []Point, stop <-chan struct{}) {
	current := "" // 最近一次处理过的人员

	for {
		if autoStopped(stop) {
			return
		}
		// 次数上限：打够了就收工（运行中把上限调小，也能在这里立刻生效）
		if autoGreetLimitReached() {
			stopAuto(fmt.Sprintf("已达到打招呼次数上限 %d 次", autoMaxGreetsNow()))
			return
		}
		setAutoState(stReadP)

		img, err := autoCapture(nameRegion)
		if err != nil {
			noteAutoError("截图失败", err)
			sleepOrStop(stop, autoInterval)
			continue
		}
		text, err := autoRecognize(img)
		if err != nil {
			noteAutoError("识别失败", err)
			sleepOrStop(stop, autoInterval)
			continue
		}
		noteAutoOK()
		name := normalizeName(text)

		if name != "" && name != current {
			// 换人了：按新人处理
			current = name
			setAutoPerson(name)
			setAutoState(stChanged)
			appendLog("新人员：%s", name)
			requestDetectUI()

			// 在线就打招呼、不在线就直接翻页，
			// 两种情况下 greetPerson 内部都已经点了下一页并停留好了。
			if greetPerson(name, onlineRegion, greetPoints, nextPoints, stop) == greetHalt {
				return // 期间被停止
			}
		} else {
			// 同名或读不出字：说明上一次翻页没生效（或者已经到底了），再翻一次。
			if name == "" {
				appendLog("没识别到姓名，重翻一次")
			} else {
				appendLog("姓名未变化（%s），重翻一次", name)
			}
			if clickNextPage(nextPoints, current, stop) {
				return // 期间被停止
			}
		}

		if sleepOrStop(stop, autoInterval) {
			return
		}
	}
}

// greetResult 表示处理完一位人员后的结果，主循环据此决定接下来怎么走。
type greetResult int

const (
	greetNexted greetResult = iota // 已翻页并停留完毕，可以进入下一轮
	greetHalt                      // 期间收到停止信号，调用方要立刻退出
)

// greetPerson 处理一位人员：先看在线状态，然后**不管在线与否都翻页到下一个用户**。
//
//	在线   -> 点「组1（打招呼按钮）」打招呼；
//	不在线 -> 什么都不点（直接翻页）。
//
// 两种情况下都会点「组2（下一页按钮）」翻到下一个：
//   - 在线的：打完招呼，按用户要求随机停 0~0.5 秒就直接翻页（不做别的判断，
//     也就不存在「打完招呼页面不跳转」的卡死问题）；
//   - 不在线的：直接翻页。
//
// 在线状态识别失败时按「不在线」处理（直接翻页），而不是把人漏掉。
// 返回值：greetNexted=已翻页，主循环去确认翻页成功；greetHalt=被停止。
func greetPerson(name string, onlineRegion Rect, greetPoints, nextPoints []Point, stop <-chan struct{}) greetResult {
	setAutoState(stCheckOnl)

	online := false
	img, err := autoCapture(onlineRegion)
	if err != nil {
		appendLog("在线状态截图失败：%s（按不在线处理，直接翻页）", err)
		requestDetectUI()
	} else if autoStopped(stop) {
		return greetHalt
	} else if text, err := autoRecognize(img); err != nil {
		appendLog("在线状态识别失败：%s（按不在线处理，直接翻页）", err)
		requestDetectUI()
	} else {
		// 在线与否只决定下一步动作，进度看状态标签就行，不写日志
		online = looksOnline(text)
	}

	if online {
		// 在线：点组1 = 打招呼
		setAutoState(stOnline)
		if doPointClick(greetPoints, name, "打招呼", true, stop) {
			return greetHalt
		}
		// 打完招呼：随机停 0~0.5 秒，然后直接翻页下一个用户（不做别的判断）
		pause := time.Duration(rand.Intn(int(greetPauseMax/time.Millisecond)+1)) * time.Millisecond
		if sleepOrStop(stop, pause) {
			return greetHalt
		}
	}
	// 不在线就什么都不点，直接往下走翻页（不留痕、不计数）

	// 不管在线与否，都翻页到下一个用户
	if clickNextPage(nextPoints, name, stop) {
		return greetHalt
	}
	return greetNexted
}

// clickNextPage 点「下一页按钮」，并在点完后随机停留 0.8~1.5 秒防人机检测。
// 返回 true 表示期间收到了停止信号。
func clickNextPage(nextPoints []Point, name string, stop <-chan struct{}) bool {
	// 翻页是每轮必经动作，不写日志（状态标签里有进度）
	if doPointClick(nextPoints, name, "下一页", false, stop) {
		return true
	}
	// 点完下一页：随机停留 0.8~1.5 秒，防止人机检测
	pause := nextPauseMin + time.Duration(rand.Intn(int(nextPauseMax-nextPauseMin)+1))
	if sleepOrStop(stop, pause) {
		return true
	}
	setAutoState(stWaitNext)
	return false
}

// doPointClick 从 pts 里随机挑一个点，随机偏移后点下去。
// label 用于日志/状态；isGreet 决定是否计入「已打招呼」次数。
// 返回 true 表示期间收到了停止信号。
func doPointClick(pts []Point, name, label string, isGreet bool, stop <-chan struct{}) bool {
	// nextClickPoint 只在 pts 为空时返回 ok=false，所以这里一个判断就够，
	// 不再单独重复写一遍 len(pts)==0 的分支。
	p, ok := nextClickPoint(pts)
	if !ok {
		appendLog("没有可用的点击点，跳过 %s", name)
		setAutoState(stWaitNext)
		return false
	}

	p = jitterPoint(p, clickJitterPx)
	if isGreet {
		setAutoState(stGreeting)
	}
	requestDetectUI()

	// 这是最后一道闸：确认没被停止，才真的动鼠标
	if autoStopped(stop) {
		return true
	}
	if err := autoClick(p.X, p.Y); err != nil {
		// 点击失败算异常，要留痕；正常打上就不记了（进度看状态标签）
		appendLog("%s点击失败：%s", label, err)
	} else {
		greeted := 0
		if isGreet {
			autoMu.Lock()
			autoGreets++
			greeted = autoGreets
			autoMu.Unlock()
		}

		// 打到设定次数就自动收工。这里是唯一给「已打招呼」加数的地方，
		// 所以收工判定放这儿最准：刚好打满 N 次，不会多打，也不会少打。
		if m := autoMaxGreetsNow(); greeted > 0 && m > 0 && greeted >= m {
			stopAuto(fmt.Sprintf("已达到打招呼次数上限 %d 次", m))
			return true
		}
	}
	return autoStopped(stop)
}

// nextClickPoint 随机取一个点击点：多处配置的点都会被用到，
// 而且不会固定成「每次都是同一处、同一个像素」。
func nextClickPoint(points []Point) (Point, bool) {
	if len(points) == 0 {
		return Point{}, false
	}
	autoMu.Lock()
	i := rand.Intn(len(points))
	if len(points) > 1 && i == autoLastPt {
		// 尽量别连续两次用同一个点
		i = (i + 1) % len(points)
	}
	autoLastPt = i
	autoMu.Unlock()
	return points[i], true
}

// noteAutoError 只在错误内容变化时记一行，避免一直失败时刷屏。
func noteAutoError(what string, err error) {
	msg := fmt.Sprintf("%s：%s", what, err)
	autoMu.Lock()
	changed := msg != autoLastErr
	autoLastErr = msg
	autoMu.Unlock()
	if changed {
		appendLog("打招呼 %s", msg)
	}
}

func noteAutoOK() {
	autoMu.Lock()
	had := autoLastErr != ""
	autoLastErr = ""
	autoMu.Unlock()
	if had {
		appendLog("打招呼重新识别到文字")
	}
}

// updateAutoUI 刷新「自动打招呼」的两行：进度行（本次已打 / 本次剩余）+ 状态行。
func updateAutoUI() {
	autoMu.Lock()
	state := autoState
	person := autoPerson
	greets := autoGreets
	autoMu.Unlock()

	// 进度单独占一行，跟状态行同源同频，事件一发生就刷新
	setWindowText(hwndAutoProg, utf16ptr(greetProgressText(greets)))

	// 状态行只说「在干什么、当前是谁」，数字都在上面那行
	if state == "" {
		setWindowText(hwndAutoStat, utf16ptr("打招呼：未开始"))
		return
	}

	text := "打招呼：" + autoStateText(state)
	if person != "" && state != stStopped {
		text += "　当前：" + person
	}
	setWindowText(hwndAutoStat, utf16ptr(text))
}
