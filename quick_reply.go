//go:build windows

// quick_reply.go 是「快捷回复」模块（界面的 B 页）。
//
// 它与「打招呼」那套流程是**两个互不相干的模块**，共用的是底层那几件事：
// 全局鼠标钩子采集点击点、点击点存 config.json、主线程刷界面。
//
// 区别在于：打招呼那套有识别与判断（读姓名、查在线，再决定点不点）；
// 这一套**不做任何识别和判断**，只是把 6 组点击点分别采集下来，
// 将来按组顺序点出去（自动化还没做，见页面上那句说明）。
//
// 组号约定：内部统一用「全局组号」 g：
//
//	g=1        打招呼页 组1（打招呼按钮）
//	g=2        打招呼页 组2（下一页按钮）
//	g=3+i      快捷回复页 第 i 组（i 从 0 起，共 quickGroupCount 组）
//
// 之所以统一成一套组号，是为了让采集钩子、点队列、日志都能直接复用；
// 界面上再按 g 拆回「哪一页的哪一组」。
package main

import (
	"fmt"
	"strings"
)

// quickFirstGroup 是快捷回复页第 1 组的全局组号。
const quickFirstGroup = 3

// quickGroup 把「快捷回复页第 i 组（0 起）」换算成全局组号。
func quickGroup(i int) int { return quickFirstGroup + i }

// quickIndexOf 把全局组号换算回「快捷回复页第 i 组（0 起）」；
// 不属于快捷回复页时返回 -1。
func quickIndexOf(g int) int {
	i := g - quickFirstGroup
	if i < 0 || i >= quickGroupCount {
		return -1
	}
	return i
}

// quickGroupName 是某一组在日志/说明里的名字。
func quickGroupName(i int) string { return fmt.Sprintf("快捷回复%d", i+1) }

// groupName 返回某个全局组号对应的功能名（日志与状态文案共用）。
func groupName(g int) string {
	switch {
	case g == 1:
		return "打招呼按钮"
	case g == 2:
		return "下一页按钮"
	}
	if i := quickIndexOf(g); i >= 0 {
		return quickGroupName(i)
	}
	return fmt.Sprintf("第%d组", g)
}

// quickGroupPoints 取快捷回复页第 i 组的点击点（0 起）。
func quickGroupPoints(i int) []Point {
	if i < 0 || i >= quickGroupCount {
		return nil
	}
	return cfg.QuickGroups[i]
}

// ---- 每组的点击间隔（毫秒）----

// quickInterval 取第 i 组配置里的间隔值。
// 输入框留空 / 填 0 / 配置里没有，都按默认值算，保证点起来一定有个合理的间隔。
func quickInterval(i int) int {
	if i < 0 || i >= quickGroupCount {
		return quickIntervalDefaultMs
	}
	n := cfg.QuickIntervals[i]
	if n <= 0 {
		return quickIntervalDefaultMs
	}
	if n > quickIntervalMaxMs {
		return quickIntervalMaxMs
	}
	return n
}

// clampQuickInterval 把用户填的数字收敛到合法范围（0 表示「用默认值」）。
func clampQuickInterval(n int) int {
	if n < 0 {
		return 0
	}
	if n > quickIntervalMaxMs {
		return quickIntervalMaxMs
	}
	return n
}

// setQuickInterval 写入第 i 组的间隔并落盘。只存用户填的原值（允许 0=默认）。
func setQuickInterval(i, n int) {
	if i < 0 || i >= quickGroupCount {
		return
	}
	n = clampQuickInterval(n)
	if cfg.QuickIntervals[i] == n {
		return
	}
	cfg.QuickIntervals[i] = n
	_ = saveConfig(cfg)
}

// syncQuickIntervalFromUI 读第 i 组输入框的间隔，同步到配置。
// 输入框留空 / 读不出数 -> 按 0 处理（也就是「用默认值」）。
//
// 回填期间（uiProgrammaticWrite）直接返回，避免把刚填进去的值读成 0 又写回配置。
func syncQuickIntervalFromUI(i int) {
	if i < 0 || i >= quickGroupCount {
		return
	}
	if hwndQkGap[i] == 0 {
		return
	}
	if uiProgrammaticWrite() {
		return
	}
	setQuickInterval(i, parseEditInt(hwndQkGap[i]))
}

// loadQuickIntervalsToUI 启动时把配置里的间隔填进 6 个输入框。
// 配置里是 0（没设过）就不填，让输入框空着，输入框右边的 hint 会说明默认值。
func loadQuickIntervalsToUI() {
	for i := 0; i < quickGroupCount; i++ {
		if hwndQkGap[i] == 0 {
			continue
		}
		if n := cfg.QuickIntervals[i]; n > 0 {
			setEditInt(hwndQkGap[i], n)
		}
	}
}

// totalClickGroups 是采集系统一共支持的组数：打招呼页 2 组 + 快捷回复页 N 组。
func totalClickGroups() int { return 2 + quickGroupCount }

// appendClickPoint 把采集到的点并入某个全局组号对应的配置切片。
// 只负责写 cfg，不落盘（落盘由 drainClicks / clearClickPoints 统一做）。
func appendClickPoint(g int, p Point) {
	switch {
	case g == 1:
		cfg.ClickPoints = append(cfg.ClickPoints, p)
	case g == 2:
		cfg.ClickPoints2 = append(cfg.ClickPoints2, p)
	default:
		if i := quickIndexOf(g); i >= 0 {
			cfg.QuickGroups[i] = append(cfg.QuickGroups[i], p)
		}
	}
}

// clearPointsOfGroup 清空某个全局组号已采集的点（含还没入队的队列）。
func clearPointsOfGroup(g int) {
	switch {
	case g == 1:
		cfg.ClickPoints = []Point{}
	case g == 2:
		cfg.ClickPoints2 = []Point{}
	default:
		i := quickIndexOf(g)
		if i < 0 {
			return // 不是有效的组号，什么都不动
		}
		cfg.QuickGroups[i] = []Point{}
	}
	clickMu.Lock()
	if g >= 1 && g <= len(clickQueues) {
		clickQueues[g-1] = nil
	}
	clickMu.Unlock()
}

// ---- 快捷回复页的界面刷新 ----

// quickStatusText 是某一组状态标签的文案（采集中由 startCaptureFlow 单独设置，这里不覆盖）。
func quickStatusText(i int) string {
	n := len(quickGroupPoints(i))
	if n == 0 {
		return fmt.Sprintf("第%d组 · 未采集", i+1)
	}
	return fmt.Sprintf("第%d组 · 已采集 %d 点", i+1, n)
}

// quickPointsText 把某一组的点击点拼成多行文本，显示在列表里。
func quickPointsText(i int) string {
	var b strings.Builder
	for n, p := range quickGroupPoints(i) {
		if n > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "%d. (%d,%d)", n+1, p.X, p.Y)
	}
	return b.String()
}

// setQuickStatus 设置某一组的状态标签。
func setQuickStatus(i int, text string) {
	if i < 0 || i >= quickGroupCount || hwndQkStat[i] == 0 {
		return
	}
	setWindowText(hwndQkStat[i], utf16ptr(text))
}

// updateQuickPage 刷新快捷回复页所有组的列表与状态（采集中那一组保留文案）。
func updateQuickPage() {
	for i := 0; i < quickGroupCount; i++ {
		if hwndQkList[i] == 0 {
			continue
		}
		if !(capturing && captureGroup == quickGroup(i)) {
			setQuickStatus(i, quickStatusText(i))
		}
		setWindowText(hwndQkList[i], utf16ptr(quickPointsText(i)))
	}
}

// ---- 页面切换 ----

// applyPage 把当前页（quickPage）落到界面上：属于该页的控件显示，另一页整批隐藏。
//
// 用显隐而不是重建控件：控件建一次就不再动，切页只是 ShowWindow，
// 不会有句柄泄漏、也不会丢失输入框里的内容。
func applyPage() {
	if quickPage {
		for _, h := range pageACtrls {
			showWindow(h, SW_HIDE)
		}
		for _, h := range pageBCtrls {
			showWindow(h, SW_SHOW)
		}
	} else {
		for _, h := range pageBCtrls {
			showWindow(h, SW_HIDE)
		}
		for _, h := range pageACtrls {
			showWindow(h, SW_SHOW)
		}
	}
	updatePageButton()
	// 切页后把该页的状态刷一遍（列表内容、采集状态等）
	if quickPage {
		updateQuickPage()
	} else {
		updateDisplay()
	}
	requestDetectUI()
}

// updatePageButton 按当前页设置切换按钮的文案（在 B 页时提示怎么回去）。
func updatePageButton() {
	if hwndPageBtn == 0 {
		return
	}
	if quickPage {
		setWindowText(hwndPageBtn, utf16ptr("返回打招呼"))
	} else {
		setWindowText(hwndPageBtn, utf16ptr("快捷回复"))
	}
}

// togglePage 点击切换按钮时切页。
//
// 切页前先把「正在采集」收尾：采集钩子同一时刻只能服务一组，页都切走了还留着采集
// 会让人找不到怎么停。
func togglePage() {
	if capturing {
		stopCaptureFlow(0)
	}
	quickPage = !quickPage
	applyPage()
}

// ---- 快捷回复页的采集 / 清空（派发用）----

// startQuickCapture 开始采集快捷回复页第 i 组（0 起）。
func startQuickCapture(i int) {
	if i < 0 || i >= quickGroupCount {
		return
	}
	startCaptureFlow(quickGroup(i))
}

// stopQuickCapture 停止采集快捷回复页第 i 组（0 起）。
func stopQuickCapture(i int) {
	if i < 0 || i >= quickGroupCount {
		return
	}
	stopCaptureFlow(quickGroup(i))
}

// clearQuickGroup 清空快捷回复页第 i 组（0 起）的点击点。
func clearQuickGroup(i int) {
	if i < 0 || i >= quickGroupCount {
		return
	}
	clearClickPoints(quickGroup(i))
}

// dispatchQuickControl 按控件 ID 派发快捷回复页的按钮 / 输入框事件。
// 按钮 ID 分三段连续排列：开始 / 停止 / 清空，各占 quickGroupCount 个号；
// 落在哪一段决定做什么，减去该段基数就是组下标。
// 间隔输入框是另一段（qkEditGapBase），只在内容变化时同步配置。
// code 是通知码（按钮的 BN_CLICKED、输入框的 EN_CHANGE）。
// 返回是否认领了这个 ID（false 交给 defWindowProc 走默认处理）。
func dispatchQuickControl(id int, code uint16) bool {
	switch {
	case id >= qkBtnStartBase && id < qkBtnStopBase:
		startQuickCapture(id - qkBtnStartBase)
	case id >= qkBtnStopBase && id < qkBtnClearBase:
		stopQuickCapture(id - qkBtnStopBase)
	case id >= qkBtnClearBase && id < qkBtnRangeEnd:
		clearQuickGroup(id - qkBtnClearBase)
	case id >= qkEditGapBase && id < qkEditGapBase+quickGroupCount:
		// 间隔改动即时生效（后续做自动化时按组取用）
		if code == EN_CHANGE {
			syncQuickIntervalFromUI(id - qkEditGapBase)
		}
	default:
		return false
	}
	return true
}
