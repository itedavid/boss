//go:build windows

// force.go 实现「强制点击」：不看页面状态，每隔 2~3 秒随机挑一个已采集的点击点点一下，
// 间隔和落点都带随机，节奏接近人手，不是固定节拍。
//
// 它和「自动打招呼」互斥：开一个会先把另一个停掉，避免两边同时抢鼠标。
// 整个循环跑在后台 goroutine 上，界面刷新靠 PostMessage 回主线程。
package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

const (
	forceMinMs        = 800 // 两次点击之间的最短间隔
	forceJitterMs     = 300 // 在最短间隔上再随机加 0~1000ms，凑成 2~3 秒
	forceLongChance   = 20   // 每 100 次里大约有 20 次会多停一下
	forceLongMinMs    = 1200 // 那次额外的停顿：1.2~4.2 秒
	forceLongJitterMs = 3000
	forceJitterPx     = 4 // 落点在配置的点附近偏 ±4 像素
)

var (
	forceMu      sync.Mutex
	forceRunning bool
	forceStopCh  chan struct{}
	forceCount   int  // 本次已经点了多少次
	forceStarted bool // 是否启动过（决定状态标签显示「未开始」还是「已停止」）
	forceLastPt  int  // 上一次用的点下标，用来避免连续两次点同一个点
)

// startForceClick 开始强制点击。开始前把同样会动鼠标的任务都停掉。
func startForceClick() {
	forceMu.Lock()
	already := forceRunning
	forceMu.Unlock()
	if already {
		return
	}

	if len(cfg.ClickPoints) == 0 {
		appendLog("强制点击失败：请先采集至少一个点击点")
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
		return
	}

	// 打招呼、区域检测、点击点采集都会和强制点击抢鼠标，先停掉
	autoMu.Lock()
	autoBusy := autoRunning
	autoMu.Unlock()
	if autoBusy {
		stopAuto("开始强制点击")
	}
	stopDetection("开始强制点击")
	if capturing {
		stopCaptureFlow()
		appendLog("强制点击期间已停止采集，避免把自动点击记成采集点")
	}

	stop := make(chan struct{})
	forceMu.Lock()
	forceRunning = true
	forceStarted = true
	forceStopCh = stop
	forceCount = 0
	forceLastPt = -1
	// 配置先快照一份给后台，避免和主线程写配置打架
	points := append([]Point(nil), cfg.ClickPoints...)
	forceMu.Unlock()

	appendLog("开始强制点击：每 %d~%dms 随机点一次（偶尔会更久），已采集点击点 %d 个",
		forceMinMs, forceMinMs+forceJitterMs, len(points))
	postMessage(hwndMain, WM_APP_DETECT, 0, 0)

	go forceLoop(points, stop)
}

// stopForceClick 停止强制点击。reason 会记进日志，方便看出是谁停的。
func stopForceClick(reason string) {
	forceMu.Lock()
	if !forceRunning {
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
	postMessage(hwndMain, WM_APP_DETECT, 0, 0)
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
			forceMu.Lock()
			forceCount++
			forceMu.Unlock()
			appendLog("强制点击：(%d,%d)", p.X, p.Y)
		}
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
	}
}

// updateForceUI 刷新强制点击的状态标签。
func updateForceUI() {
	forceMu.Lock()
	running := forceRunning
	started := forceStarted
	count := forceCount
	forceMu.Unlock()

	state := "未开始"
	switch {
	case running:
		state = "运行中"
	case started:
		state = "已停止"
	}
	setWindowText(hwndForceStat, utf16ptr(fmt.Sprintf(
		"强制点击：%s　已点击 %d 次（每 %d~%d 秒随机点一个）",
		state, count, forceMinMs/1000, (forceMinMs+forceJitterMs)/1000)))
}
