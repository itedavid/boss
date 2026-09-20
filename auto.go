//go:build windows

// auto.go 负责 V0.6 的「自动打招呼」状态机：
//
//	等待人员 -> 识别人员 -> 确认换人 -> 检查在线 -> 在线 -> 打招呼 -> 等待下一人员
//
// 另外有一个「只按姓名判断」模式：跳过「检查在线」这一步，确认换人就直接点。
//
// 整个循环跑在后台 goroutine 上，不阻塞界面；所有界面刷新都靠 PostMessage 回主线程。
// 停止是「硬停止」：点完 [停止] 或按 Esc 之后，既不再截图识别，也不会再点鼠标。
package main

import (
	"fmt"
	"math/rand"
	"sync"
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

// 打招呼的两种模式：
//
//	autoModeOnline   —— 先看「在线区域」的字，只有在线才点（默认）
//	autoModeNameOnly —— 只看姓名区域：只要确认换了人就直接点，不检查在线状态
const (
	autoModeOnline   = "online"
	autoModeNameOnly = "nameonly"
)

// 点完一次后多等一会儿，让页面把下一位刷出来，避免立刻重复识别到同一个人。
const (
	autoAfterGreet  = 1500 * time.Millisecond // 打招呼之后的基础等待时间
	greetWaitJitter = 1200 * time.Millisecond // 在这之上再随机加 0~1200ms
	clickJitterPx   = 4                       // 点击位置在配置的点附近随机偏 ±4 像素
)

// jitterDuration 在 d 的基础上随机加一点（最多 extra），别每次都卡在同一个节拍上。
func jitterDuration(d, extra time.Duration) time.Duration {
	ms := int(extra / time.Millisecond)
	if ms <= 0 {
		return d
	}
	return d + time.Duration(rand.Intn(ms))*time.Millisecond
}

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
	autoSkips   int    // 因为不在线而跳过的次数
	autoLastPt  int    // 上一次用的点击点下标（下一次尽量换一个）
	autoLastErr string // 上一次的错误信息，避免刷屏
	autoMode    string // 本次运行用的是哪种模式
)

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
	postMessage(hwndMain, WM_APP_DETECT, 0, 0)
}

func setAutoPerson(name string) {
	autoMu.Lock()
	autoPerson = name
	autoMu.Unlock()
}

// startAuto 开始自动打招呼。开始前先把该有的配置都检查一遍。
// mode 取 autoModeOnline 或 autoModeNameOnly。
func startAuto(mode string) {
	autoMu.Lock()
	if autoRunning {
		autoMu.Unlock()
		return
	}
	autoMu.Unlock()

	if !rectIsSet(cfg.NameRegion) {
		appendLog("开始打招呼失败：请先框选求职者姓名区域")
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
		return
	}
	if mode == autoModeOnline && !rectIsSet(cfg.OnlineRegion) {
		appendLog("开始打招呼失败：请先框选在线状态区域（只按姓名判断的模式不用框）")
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
		return
	}
	if len(cfg.ClickPoints) == 0 {
		appendLog("开始打招呼失败：请先采集至少一个点击点")
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
		return
	}

	// 强制点击、V0.5 检测都会和打招呼抢鼠标，先停掉
	stopForceClick("自动打招呼开始")
	stopDetection("自动打招呼开始")

	stop := make(chan struct{})
	autoMu.Lock()
	autoRunning = true
	autoStopCh = stop
	autoState = stWaiting
	autoPerson = ""
	autoGreets, autoSkips = 0, 0
	autoLastErr = ""
	autoMode = mode
	autoMu.Unlock()

	if mode == autoModeNameOnly {
		appendLog("开始打招呼（只按姓名判断，不看在线的字）：求职者姓名区域 %d,%d - %d,%d；点击点 %d 个",
			cfg.NameRegion.Left, cfg.NameRegion.Top, cfg.NameRegion.Right, cfg.NameRegion.Bottom,
			len(cfg.ClickPoints))
	} else {
		appendLog("开始打招呼：求职者姓名区域 %d,%d - %d,%d；在线状态区域 %d,%d - %d,%d；点击点 %d 个",
			cfg.NameRegion.Left, cfg.NameRegion.Top, cfg.NameRegion.Right, cfg.NameRegion.Bottom,
			cfg.OnlineRegion.Left, cfg.OnlineRegion.Top, cfg.OnlineRegion.Right, cfg.OnlineRegion.Bottom,
			len(cfg.ClickPoints))
	}
	setAutoState(stWaiting)

	// 配置在启动时快照一份给后台用，避免和主线程写配置打架
	nameRegion := cfg.NameRegion
	onlineRegion := cfg.OnlineRegion
	points := append([]Point(nil), cfg.ClickPoints...)

	go autoLoop(nameRegion, onlineRegion, points, stop, mode == autoModeOnline)
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
	postMessage(hwndMain, WM_APP_DETECT, 0, 0)
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

// autoLoop 是自动打招呼主循环。checkOnline 为 false 时只看姓名，不读在线区域。
func autoLoop(nameRegion, onlineRegion Rect, points []Point, stop <-chan struct{}, checkOnline bool) {
	current := "" // 当前已确认的人员
	pending := "" // 待确认的新结果
	pendingN := 0 // 待确认结果连续出现次数

	for {
		if autoStopped(stop) {
			return
		}
		setAutoState(stReadP)

		img, err := autoCapture(nameRegion)
		if err != nil {
			noteAutoError("截图失败", err)
		} else if text, err := autoRecognize(img); err != nil {
			noteAutoError("识别失败", err)
		} else {
			noteAutoOK()
			name := normalizeName(text)
			switch {
			case name == "":
				// 没识别到字：继续等待，不动当前人员

			case current == "" && pending == "":
				current = name
				setAutoPerson(name)
				appendLog("当前人员：%s", name)
				postMessage(hwndMain, WM_APP_DETECT, 0, 0)

			case name == current:
				if pending != "" {
					appendLog("恢复为原人员：%s（丢弃待确认 %s）", name, pending)
					postMessage(hwndMain, WM_APP_DETECT, 0, 0)
				}
				pending, pendingN = "", 0

			case name == pending:
				pendingN++
				if pendingN >= detectConfirm {
					current = name
					pending, pendingN = "", 0
					setAutoPerson(name)
					setAutoState(stChanged)
					appendLog("检测到新人员：%s", name)
					postMessage(hwndMain, WM_APP_DETECT, 0, 0)

					if greetPerson(name, onlineRegion, points, stop, checkOnline) {
						return // 期间被停止
					}
					if sleepOrStop(stop, jitterDuration(autoAfterGreet, greetWaitJitter)) {
						return
					}
				} else {
					appendLog("疑似换人：%s（%d/%d）", name, pendingN, detectConfirm)
					postMessage(hwndMain, WM_APP_DETECT, 0, 0)
				}

			default:
				pending = name
				pendingN = 1
				appendLog("出现新结果：%s（1/%d）", name, detectConfirm)
				postMessage(hwndMain, WM_APP_DETECT, 0, 0)
			}
		}

		if sleepOrStop(stop, detectInterval) {
			return
		}
	}
}

// greetPerson 处理一位新人员：查在线 -> 在线就点一下。
// checkOnline 为 false 时跳过在线检查，确认换人就点。
// 返回 true 表示期间收到了停止信号（调用方要立刻退出）。
func greetPerson(name string, onlineRegion Rect, points []Point, stop <-chan struct{}, checkOnline bool) bool {
	if checkOnline {
		setAutoState(stCheckOnl)

		img, err := autoCapture(onlineRegion)
		if err != nil {
			appendLog("在线状态截图失败：%s（跳过 %s）", err, name)
			postMessage(hwndMain, WM_APP_DETECT, 0, 0)
			return false
		}
		if autoStopped(stop) {
			return true
		}

		text, err := autoRecognize(img)
		if err != nil {
			appendLog("在线状态识别失败：%s（跳过 %s）", err, name)
			postMessage(hwndMain, WM_APP_DETECT, 0, 0)
			return false
		}

		verdict := onlineVerdict(text)
		appendLog("在线状态：%s（%s）", verdict, name)
		if !looksOnline(text) {
			autoMu.Lock()
			autoSkips++
			autoMu.Unlock()
			setAutoState(stWaitNext)
			return false
		}
	} else {
		appendLog("只按姓名判断：不等在线的字，直接打招呼（%s）", name)
	}

	setAutoState(stOnline)
	if autoStopped(stop) {
		return true
	}

	p, ok := nextClickPoint(points)
	if !ok {
		appendLog("没有可用的点击点，跳过 %s", name)
		setAutoState(stWaitNext)
		return false
	}

	p = jitterPoint(p, clickJitterPx)
	setAutoState(stGreeting)
	appendLog("执行打招呼点击：(%d,%d)", p.X, p.Y)
	postMessage(hwndMain, WM_APP_DETECT, 0, 0)

	// 这是最后一道闸：确认没被停止，才真的动鼠标
	if autoStopped(stop) {
		return true
	}
	if err := autoClick(p.X, p.Y); err != nil {
		appendLog("点击失败：%s", err)
	} else {
		autoMu.Lock()
		autoGreets++
		autoMu.Unlock()
		appendLog("打招呼完成：%s", name)
	}
	if autoStopped(stop) {
		return true
	}

	setAutoState(stWaitNext)
	return false
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

// updateAutoUI 刷新「自动打招呼」的状态标签。
func updateAutoUI() {
	autoMu.Lock()
	state := autoState
	person := autoPerson
	greets, skips := autoGreets, autoSkips
	mode := autoMode
	autoMu.Unlock()

	if state == "" {
		setWindowText(hwndAutoStat, utf16ptr("打招呼：未开始"))
		return
	}

	label := "打招呼："
	if mode == autoModeNameOnly {
		label = "打招呼（只按姓名）："
	}
	text := label + autoStateText(state)
	if person != "" && state != stStopped {
		text += "　当前：" + person
	}
	text += fmt.Sprintf("　已打招呼 %d / 跳过 %d", greets, skips)
	setWindowText(hwndAutoStat, utf16ptr(text))
}
