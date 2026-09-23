//go:build windows

// force.go 实现「强制点击」：不看页面状态，每隔 2~3 秒随机挑一个已采集的点击点点一下，
// 间隔和落点都带随机，节奏接近人手，不是固定节拍。
//
// 它和「自动打招呼」互斥：开一个会先把另一个停掉，避免两边同时抢鼠标。
// 整个循环跑在后台 goroutine 上，界面刷新靠 PostMessage 回主线程。
package main

import (
	"math/rand"
	"sync"
	"time"
)

const (
	forceMinMs        = 800  // 两次点击之间的最短间隔
	forceJitterMs     = 300  // 在最短间隔上再随机加 0~1000ms，凑成 2~3 秒
	forceLongChance   = 20   // 每 100 次里大约有 20 次会多停一下
	forceLongMinMs    = 1200 // 那次额外的停顿：1.2~4.2 秒
	forceLongJitterMs = 3000
	forceJitterPx     = 4 // 落点在配置的点附近偏 ±4 像素
)

var (
	forceMu      sync.Mutex
	forceRunning bool
	forceStopCh  chan struct{}
	forceLastPt  int // 上一次用的点下标，用来避免连续两次点同一个点
	forceGroup   int // 当前强制点击用的是第几组点击点（1/2），0 表示没在跑
)

// startForceClick 开始某一组的强制点击（group=1/2），用对应组已采集的点击点。
// 强制点击同一时刻只能有一组在跑：若已有别的组在跑，会先把它停掉再切到这一组。
func startForceClick(group int) {
	if group < 1 || group > 2 {
		return
	}
	forceMu.Lock()
	if forceRunning {
		// 先停掉当前在跑的那一组（切组）
		forceRunning = false
		close(forceStopCh)
		forceStopCh = nil
	}
	var pts []Point
	if group == 1 {
		pts = cfg.ClickPoints
	} else {
		pts = cfg.ClickPoints2
	}
	if len(pts) == 0 {
		forceMu.Unlock()
		appendLog("强制点击%s 失败：请先采集至少一个点击点", groupName(group))
		requestDetectUI()
		return
	}

	stop := make(chan struct{})
	forceRunning = true
	forceStopCh = stop
	forceLastPt = -1
	forceGroup = group
	// 配置先快照一份给后台，避免和主线程写配置打架
	points := append([]Point(nil), pts...)
	forceMu.Unlock()

	// 打招呼、点击点采集都会和强制点击抢鼠标，先停掉
	autoMu.Lock()
	autoBusy := autoRunning
	autoMu.Unlock()
	if autoBusy {
		stopAuto("开始强制点击")
	}
	if capturing {
		stopCaptureFlow(0)
		appendLog("强制点击%s 期间已停止采集，避免把自动点击记成采集点", groupName(group))
	}

	appendLog("开始强制点击%s：每 %d~%dms 随机点一次（偶尔会更久），已采集点击点 %d 个",
		groupName(group), forceMinMs, forceMinMs+forceJitterMs, len(points))
	requestDetectUI()

	safeGo("forceLoop", func() { forceLoop(points, stop) })
}

// stopForceClick 停止强制点击。group 传 0 表示停掉当前在跑的那一组（Esc / 退出用），
// 传 1/2 只在该组正在跑时才停。reason 会记进日志，方便看出是谁停的。
func stopForceClick(group int, reason string) {
	forceMu.Lock()
	if !forceRunning {
		forceMu.Unlock()
		return
	}
	if group != 0 && forceGroup != group {
		forceMu.Unlock()
		return
	}
	forceRunning = false
	close(forceStopCh)
	forceStopCh = nil
	forceMu.Unlock()

	if reason != "" {
		appendLog("停止强制点击（%s）", reason)
	} else {
		appendLog("停止强制点击")
	}
	requestDetectUI()
}

// toggleForceClick 切换某一组强制点击的开关：该组正在跑就停，没跑就开。
func toggleForceClick(group int) {
	if group < 1 || group > 2 {
		return
	}
	forceMu.Lock()
	active := forceRunning && forceGroup == group
	forceMu.Unlock()
	if active {
		stopForceClick(group, "")
	} else {
		startForceClick(group)
	}
}

// forceClickFn 是「真正把这一点点下去」的实现。正常运行时就是 humanClick，
// 测试里换成假实现，就能不动真实鼠标地验证循环。
var forceClickFn = humanClick

// forceStopped 检查停止信号，和 autoStopped 一样只是读一个 channel。
func forceStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// forceInterval 算下一次点击要等多久：基础 2~3 秒，
// 偶尔（约五分之一）再多停一会儿，免得节奏太机械。
func forceInterval() time.Duration {
	ms := forceMinMs + rand.Intn(forceJitterMs+1)
	if rand.Intn(100) < forceLongChance {
		ms += forceLongMinMs + rand.Intn(forceLongJitterMs+1)
	}
	return time.Duration(ms) * time.Millisecond
}

// nextForcePoint 随机挑一个点击点，尽量不连续两次用同一个。
func nextForcePoint(points []Point) (Point, bool) {
	if len(points) == 0 {
		return Point{}, false
	}
	forceMu.Lock()
	i := rand.Intn(len(points))
	if len(points) > 1 && i == forceLastPt {
		i = (i + 1) % len(points)
	}
	forceLastPt = i
	forceMu.Unlock()
	return jitterPoint(points[i], forceJitterPx), true
}

// humanClick 先挪到目标附近停一下，再落到目标上点击，
// 比「一步到位」更像人手移过去的。
func humanClick(p Point) error {
	near := Point{X: p.X + rand.Intn(41) - 20, Y: p.Y + rand.Intn(41) - 20}
	if err := moveTo(near.X, near.Y); err != nil {
		return err
	}
	time.Sleep(time.Duration(30+rand.Intn(60)) * time.Millisecond)
	return clickAt(p.X, p.Y)
}

// forceLoop 是强制点击主循环：等一会儿 -> 随机挑点 -> 点一下。
func forceLoop(points []Point, stop <-chan struct{}) {
	for {
		if sleepOrStop(stop, forceInterval()) {
			return
		}
		if forceStopped(stop) {
			return
		}
		p, ok := nextForcePoint(points)
		if !ok {
			return
		}
		// 动手之前最后确认一次没被停止
		if forceStopped(stop) {
			return
		}
		if err := forceClickFn(p); err != nil {
			appendLog("强制点击失败：%s", err)
		} else {
			appendLog("强制点击：(%d,%d)", p.X, p.Y)
		}
		requestDetectUI()
	}
}

// updateForceUI 根据强制点击运行状态，刷新顶部「强制点击打招呼/下一页」按钮的文案：
// 正在跑的那一组显示为「停止强制XX」，另一组显示「强制点击XX」。
func updateForceUI() {
	forceMu.Lock()
	running := forceRunning
	grp := forceGroup
	forceMu.Unlock()

	labels := [2]string{"强制点击打招呼", "强制点击下一页"}
	if running && grp >= 1 && grp <= 2 {
		if grp == 1 {
			labels[0] = "停止强制打招呼"
		} else {
			labels[1] = "停止强制下一页"
		}
	}
	if hwndForceBtn1 != 0 {
		setWindowText(hwndForceBtn1, utf16ptr(labels[0]))
	}
	if hwndForceBtn2 != 0 {
		setWindowText(hwndForceBtn2, utf16ptr(labels[1]))
	}
}
