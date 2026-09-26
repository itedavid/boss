//go:build windows
//
// click_quick.go 是「快捷回复页轮流点击」的执行器。
//
// 规则（和老兄确认过的）：
//  1. 按组序 1→2→…→6 轮流，一组一组的点；
//  2. 该组一个点都没采集到就整组跳过，不点也不等；
//  3. 每点一下，就等「这个点所属那一组」自己配的随机间隔（界面上那行「间隔 [最小] ~ [最大] ms」，
//     在区间内随机取，固定节奏容易被系统检测）；
//  4. 6 组跑完算一轮，回到第 1 组继续，无限循环，只能手动停（界面按钮 / Esc）。
//
// 点击用前台真实鼠标（humanClick，和「强制点击」同一套），所以跑起来会占用鼠标；
// 它和自动打招呼 / 强制点击互斥，开一个会先把别的停掉，免得两头抢鼠标。
//
// 整个循环跑在后台 goroutine 上，界面刷新照旧 PostMessage 回主线程。
package main

import (
	"math/rand"
	"sync"
	"time"
)

var (
	qkRunMu      sync.Mutex
	qkRunning    bool
	qkStopCh     chan struct{}
	qkRoundCount int // 已经跑完的轮数，只用于日志
)

// qkRunningNow 返回当前是否在跑（主线程刷按钮文案用）。
func qkRunningNow() bool {
	qkRunMu.Lock()
	defer qkRunMu.Unlock()
	return qkRunning
}

// qkSnapshot 把各组已采集的点拷一份出来当快照。
//
// 为什么快照：后台循环会跑很久，期间用户可能在界面上清空某组或又采集新的点，
// 直接读 cfg 会和主线程写配置打架。快照一份既避开了竞态，
// 也保证「一轮」用的是同一批点，不会点着点着组里的点变多。
func qkSnapshot() [][]Point {
	snap := make([][]Point, quickGroupCount)
	for i := 0; i < quickGroupCount; i++ {
		pts := quickGroupPoints(i)
		if len(pts) > 0 {
			snap[i] = append([]Point(nil), pts...)
		}
	}
	return snap
}

// qkActiveGroups 从快照里算出「有点的组」的下标，没点的组直接跳过。
func qkActiveGroups(snap [][]Point) []int {
	var active []int
	for i := 0; i < quickGroupCount; i++ {
		if len(snap[i]) > 0 {
			active = append(active, i)
		}
	}
	return active
}

// startQuickClick 开始轮流点击快捷回复页 6 组已采集的坐标点。
func startQuickClick() {
	qkRunMu.Lock()
	if qkRunning {
		qkRunMu.Unlock()
		return
	}

	snap := qkSnapshot()
	active := qkActiveGroups(snap)
	if len(active) == 0 {
		qkRunMu.Unlock()
		appendLog("开始轮流点击失败：6 组都还没采集到点，请先在任意一组点「采集」")
		requestDetectUI()
		return
	}

	stop := make(chan struct{})
	qkRunning = true
	qkStopCh = stop
	qkRoundCount = 0
	qkRunMu.Unlock()

	// 和自动打招呼 / 强制点击互斥：它们同样抢鼠标，先停掉。
	autoMu.Lock()
	autoBusy := autoRunning
	autoMu.Unlock()
	if autoBusy {
		stopAuto("开始轮流点击")
	}
	forceMu.Lock()
	forceBusy := forceRunning
	forceMu.Unlock()
	if forceBusy {
		stopForceClick(0, "开始轮流点击")
	}
	// 采集期间点鼠标会被钩子记成新的采集点，先停采集。
	if capturing {
		stopCaptureFlow(0)
		appendLog("轮流点击期间已停止采集，避免把自动点击记成采集点")
	}

	appendLog("开始轮流点击：参与 %d 组（%s），每轮每组随机挑 1 个点，无限循环，点「停止点击」或按 Esc 结束",
		len(active), qkActiveGroupNames(active))
	requestDetectUI()

	safeGo("quickClickLoop", func() { quickClickLoop(snap, active, stop) })
}

// stopQuickClick 停止轮流点击。reason 会记进日志，方便看出是谁停的。
func stopQuickClick(reason string) {
	qkRunMu.Lock()
	if !qkRunning {
		qkRunMu.Unlock()
		return
	}
	qkRunning = false
	close(qkStopCh)
	qkStopCh = nil
	rounds := qkRoundCount
	qkRunMu.Unlock()

	if reason != "" {
		appendLog("停止轮流点击（%s），共跑完 %d 轮", reason, rounds)
	} else {
		appendLog("停止轮流点击，共跑完 %d 轮", rounds)
	}
	requestDetectUI()
}

// toggleQuickClick 切换轮流点击的开关：正在跑就停，没跑就开。
func toggleQuickClick() {
	if qkRunningNow() {
		stopQuickClick("")
	} else {
		startQuickClick()
	}
}

// qkStopped 检查停止信号（和 forceStopped 一样，只是读一个 channel）。
func qkStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// quickClickLoop 是轮流点击主循环。
//
// 每一轮里，每个「有点的组」只**随机挑一个点**点一次，然后换下一组；
// 6 组各点完一次就算跑完一轮，回到第 1 组继续。
// 所以一轮最多点 len(active) 下，不是把每组所有点都点一遍。
func quickClickLoop(snap [][]Point, active []int, stop <-chan struct{}) {
	for {
		if qkStopped(stop) {
			return
		}
		// ---- 一轮：按组序 1→6，每组随机挑一个点点一下 ----
		for _, i := range active {
			// 点下去之前确认没被停
			if qkStopped(stop) {
				return
			}
			// 本组随机挑一个点（用不到不重复，随机就行——组内点少，重复也无所谓）
			p := snap[i][rand.Intn(len(snap[i]))]
			// 这一次等待也在本组配的区间内随机取，避免固定节奏被系统检测
			wait := quickRandInterval(i)
			if err := humanClick(p); err != nil {
				appendLog("轮流点击失败（%s）：%s", quickGroupName(i), err)
			} else {
				appendLog("轮流点击：%s -> (%d,%d)（本组 %d 个点里随机，等待 %d ms）",
					quickGroupName(i), p.X, p.Y, len(snap[i]), wait)
			}
			requestDetectUI()

			// 点完等这一组自己配的随机间隔，再点下一组。
			if sleepOrStop(stop, time.Duration(wait)*time.Millisecond) {
				return
			}
		}
		// ---- 一轮结束 ----
		qkRunMu.Lock()
		qkRoundCount++
		qkRunMu.Unlock()
		appendLog("轮流点击：第 %d 轮完成，回到第 1 组继续", qkRunCount())
		requestDetectUI()
	}
}

// qkRunCount 读已完成的轮数（加锁，供日志使用）。
func qkRunCount() int {
	qkRunMu.Lock()
	defer qkRunMu.Unlock()
	return qkRoundCount
}

// qkActiveGroupNames 把活跃组下标拼成「第2组、第3组」这样的说明文本。
func qkActiveGroupNames(active []int) string {
	s := ""
	for n, i := range active {
		if n > 0 {
			s += "、"
		}
		s += quickGroupName(i)
	}
	return s
}

// ---- 界面 ----

// updateQuickRunUI 按轮流点击的运行状态刷新 B 页那个「开始点击 / 停止点击」按钮的文案。
func updateQuickRunUI() {
	if hwndQkRunBtn == 0 {
		return
	}
	if qkRunningNow() {
		setWindowText(hwndQkRunBtn, utf16ptr("停止点击"))
	} else {
		setWindowText(hwndQkRunBtn, utf16ptr("开始点击"))
	}
}
