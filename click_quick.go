//go:build windows
//
// click_quick.go 是「快捷回复页轮流点击」的执行器。
//
// 规则（和老兄确认过的）：
//  1. 按组序 1→2→…→8 轮流，一组一组的点；
//  2. 该组一个点都没采集到就整组跳过，不点也不等；
//  3. 每点一下，就等「这个点所属那一组」自己配的随机间隔（界面上那行「间隔 [最小] ~ [最大] ms」，
//     在区间内随机取，固定节奏容易被系统检测）；
//  4. 8 组跑完算一轮，回到第 1 组继续。
//
// 在「轮」之上再套一层「批」：
//   - 每批开始时随机生成 15～25 轮的目标轮数；
//   - 跑满目标轮数进入批间休息（分钟数在 B 页底部配的 [Min, Max] 内随机取；都填 0 = 不休息）；
//   - 休息结束后重新随机下一批的目标轮数，自动继续；
//   - 一直循环，只能手动停（界面按钮 / Esc / Ctrl+C）。
//
// 点击用前台真实鼠标（humanClick，和「强制点击」同一套），所以跑起来会占用鼠标；
// 它和自动打招呼 / 强制点击互斥，开一个会先把别的停掉，免得两头抢鼠标。
//
// 整个循环跑在后台 goroutine 上，界面刷新照旧 PostMessage 回主线程。
package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var (
	qkRunMu       sync.Mutex
	qkRunning     bool
	qkStopCh      chan struct{}
	qkTotalRounds int  // 累计跑完的总轮数（跨批），供日志与状态显示
	qkBatchCount  int  // 已经开始的批次序号（从 1 起）
	qkBatchTarget int  // 当前批的目标轮数（15～25）
	qkBatchDone   int  // 当前批已完成的轮数
	qkResting     bool // 当前是否处在批间休息
	qkRestMinutes int  // 本次批间休息的总分钟数
	qkRestRemain  int  // 本次批间休息剩余分钟数
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

// quickRandBatchTarget 随机生成一批的目标轮数：在 [quickBatchRoundMin, quickBatchRoundMax] 内取整数。
func quickRandBatchTarget() int {
	return quickBatchRoundMin + rand.Intn(quickBatchRoundMax-quickBatchRoundMin+1)
}

// startQuickClick 开始轮流点击快捷回复页 8 组已采集的坐标点。
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
		appendLog("开始轮流点击失败：8 组都还没采集到点，请先在任意一组点「采集」")
		requestDetectUI()
		return
	}

	stop := make(chan struct{})
	qkRunning = true
	qkStopCh = stop
	qkTotalRounds = 0
	qkBatchCount = 0
	qkBatchTarget = 0
	qkBatchDone = 0
	qkResting = false
	qkRestMinutes = 0
	qkRestRemain = 0
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

	lo, hi := quickPauseRange()
	if lo == 0 && hi == 0 {
		appendLog("开始轮流点击：参与 %d 组（%s），每轮每组随机挑 1 个点；每批 %d~%d 轮，未设批间休息；点「停止点击」或按 Esc 结束",
			len(active), qkActiveGroupNames(active), quickBatchRoundMin, quickBatchRoundMax)
	} else {
		appendLog("开始轮流点击：参与 %d 组（%s），每轮每组随机挑 1 个点；每批 %d~%d 轮，批间休息 %d~%d 分钟；点「停止点击」或按 Esc 结束",
			len(active), qkActiveGroupNames(active), quickBatchRoundMin, quickBatchRoundMax, lo, hi)
	}
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
	rounds := qkTotalRounds
	qkResting = false
	qkRestRemain = 0
	qkRunMu.Unlock()

	if reason != "" {
		appendLog("停止轮流点击（%s），累计跑完 %d 轮", reason, rounds)
	} else {
		appendLog("停止轮流点击，累计跑完 %d 轮", rounds)
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

// quickClickLoop 是轮流点击主循环，按「批」组织。
//
// 每一批：先随机一个目标轮数（15～25），然后一轮一轮跑；
// 每一轮里，每个「有点的组」只**随机挑一个点**点一次，然后换下一组；
// 8 组各点完一次就算跑完一轮，回到第 1 组继续。
// 跑满目标轮数后进入批间休息（分钟随机、可为 0），休息完再开下一批，无限循环。
func quickClickLoop(snap [][]Point, active []int, stop <-chan struct{}) {
	for {
		if qkStopped(stop) {
			return
		}

		// ---- 开新一批：随机目标轮数 ----
		target := quickRandBatchTarget()
		qkRunMu.Lock()
		qkBatchCount++
		batchNo := qkBatchCount
		qkBatchTarget = target
		qkBatchDone = 0
		qkResting = false
		qkRunMu.Unlock()
		appendLog("轮流点击：开始第 %d 批，目标 %d 轮", batchNo, target)
		requestDetectUI()

		// ---- 跑满目标轮数 ----
		for qkBatchRoundIndex := 0; qkBatchRoundIndex < target; qkBatchRoundIndex++ {
			if qkStopped(stop) {
				return
			}
			// 一轮：按组序 1→8，每组随机挑一个点点一下
			for _, i := range active {
				if qkStopped(stop) {
					return
				}
				// 本组随机挑一个点（组内点少，重复也无所谓）
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
			// 一轮结束
			qkRunMu.Lock()
			qkBatchDone++
			qkTotalRounds++
			done := qkBatchDone
			total := qkTotalRounds
			qkRunMu.Unlock()
			appendLog("轮流点击：第 %d 批第 %d/%d 轮完成（累计 %d 轮）", batchNo, done, target, total)
			requestDetectUI()
		}

		// ---- 一批跑完：批间休息 ----
		if qkStopped(stop) {
			return
		}
		if quickClickBatchRest(batchNo, stop) {
			return
		}
	}
}

// quickClickBatchRest 执行一次批间休息：随机分钟数、逐分钟倒计时、期间可被停止打断。
// 返回 true 表示休息期间收到了停止信号，调用方应立即退出循环。
func quickClickBatchRest(batchNo int, stop <-chan struct{}) bool {
	mins := quickRandPauseMinutes()
	if mins <= 0 {
		appendLog("轮流点击：第 %d 批完成，未设置批间休息，直接开始下一批", batchNo)
		requestDetectUI()
		return false
	}

	qkRunMu.Lock()
	qkResting = true
	qkRestMinutes = mins
	qkRestRemain = mins
	qkRunMu.Unlock()
	appendLog("轮流点击：第 %d 批完成，批间休息 %d 分钟", batchNo, mins)
	requestDetectUI()

	// 逐分钟睡，既能在状态里倒计时，也能及时响应停止。
	for m := mins; m > 0; m-- {
		qkRunMu.Lock()
		qkRestRemain = m
		qkRunMu.Unlock()
		requestDetectUI()
		if sleepOrStop(stop, time.Minute) {
			return true
		}
	}

	qkRunMu.Lock()
	qkResting = false
	qkRestRemain = 0
	qkRunMu.Unlock()
	appendLog("轮流点击：批间休息结束，开始下一批")
	requestDetectUI()
	return false
}

// qkActiveGroupNames 把活跃组下标拼成「快捷回复2、快捷回复3」这样的说明文本。
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

// updateQuickRunUI 按轮流点击的运行状态刷新 B 页那个「开始点击 / 停止点击」按钮的文案，
// 以及下面那行运行状态（本批进度 / 累计轮数 / 批间休息）。
func updateQuickRunUI() {
	if hwndQkRunBtn != 0 {
		if qkRunningNow() {
			setWindowText(hwndQkRunBtn, utf16ptr("停止点击"))
		} else {
			setWindowText(hwndQkRunBtn, utf16ptr("开始点击"))
		}
	}
	if hwndQkBatchStat != 0 {
		setWindowText(hwndQkBatchStat, utf16ptr(qkBatchStatusText()))
	}
}

// qkBatchStatusText 生成 B 页运行状态行的文案。
func qkBatchStatusText() string {
	qkRunMu.Lock()
	defer qkRunMu.Unlock()
	if !qkRunning {
		if qkTotalRounds > 0 {
			return fmt.Sprintf("状态：已停止　累计完成 %d 轮", qkTotalRounds)
		}
		return "状态：未开始"
	}
	if qkResting {
		return fmt.Sprintf("批间休息中：本次 %d 分钟（剩余 %d 分钟）　累计完成 %d 轮",
			qkRestMinutes, qkRestRemain, qkTotalRounds)
	}
	return fmt.Sprintf("运行中：第 %d 批 %d/%d 轮　累计完成 %d 轮",
		qkBatchCount, qkBatchDone, qkBatchTarget, qkTotalRounds)
}
