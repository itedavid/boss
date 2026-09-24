//go:build windows

// cap_flow.go 是「点击点采集」的界面流程层：开始/停止/清空某一组的采集，以及把已采集的点
// 显示到对应的状态标签和列表里。
//
// 底层的活儿在别处：钩子安装/卸载在 cap_hook.go，点在页面上的落点在 cap_hook.go 的队列里，
// 组与组之间的独立关系在 quick_reply.go（它定义了「全局组号」这套约定）。
// 本文件只负责把「用户按了某一页某一组的按钮」翻译成对钩子的操作 + 界面反馈。
package main

import (
	"fmt"
	"strings"
)

// groupStat / groupList 取某一组的状态标签 / 点列表控件。
// 只服务「打招呼页」的 1/2 两组；快捷回复页的控件在 hwndQkStat / hwndQkList 里。
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

// groupPoints 把某一组（打招呼页 1/2）的点击点拼成多行文本。
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

// capStatusText 某一组（打招呼页 1/2）的简短状态文案
// （采集中由 startCaptureFlow 单独设置，这里不覆盖）。
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
// 打招呼页两组用采集区标签，快捷回复页的组用 B 页标签——这样上面那些
// 采集流程函数不用关心自己是在给哪一页的组干活。
func setGroupStatus(g int, text string) {
	if i := quickIndexOf(g); i >= 0 {
		setQuickStatus(i, text)
		return
	}
	if g == 1 || g == 2 {
		setWindowText(groupStat(g), utf16ptr(text))
	}
}

// clearClickPoints 清空某一组已采集的点击点（包括还没入队的那些）。
func clearClickPoints(g int) {
	clearPointsOfGroup(g)
	updateDisplay()
	updateQuickPage()
	_ = saveConfig(cfg)
}

// startCaptureFlow 启动某一组的全局鼠标监听（采集点击点）。
// g 是全局组号（见 quick_reply.go）：1/2=打招呼页两组，>=3=快捷回复页各组。
// 若别的组正在采集，会先收尾那一组再切换，因为全局钩子同一时刻只能服务一组。
func startCaptureFlow(g int) {
	if g < 1 || g > totalClickGroups() {
		return
	}
	if capturing && captureGroup == g {
		return
	}
	if capturing {
		drainClicks(captureGroup) // 先保存当前组的点
		stopCapture()
	}
	// 强制点击会动鼠标，开始采集前先把强制点击停掉，避免抢鼠标
	stopForceClick(0, "开始采集")
	// 轮流点击同理，不停掉会把自动点击记成新的采集点
	stopQuickClick("开始采集")
	if !startCapture(g) {
		setGroupStatus(g, "启动失败：全局鼠标监听安装不上")
		return
	}
	setGroupStatus(g, fmt.Sprintf("采集中：点页面上 %s 的位置，Esc 或 [停止] 结束", groupName(g)))
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
	if g < 1 || g > totalClickGroups() {
		return false
	}
	var pts *[]Point
	switch {
	case g == 1:
		pts = &cfg.ClickPoints
	case g == 2:
		pts = &cfg.ClickPoints2
	default:
		i := quickIndexOf(g)
		if i < 0 {
			return false
		}
		pts = &cfg.QuickGroups[i]
	}
	if len(*pts) == 0 {
		return false
	}
	*pts = (*pts)[:len(*pts)-1]
	updateDisplay()
	updateQuickPage()
	_ = saveConfig(cfg)
	return true
}
