//go:build windows

// ui_refresh.go 是界面刷新的两个入口。后台 goroutine 不直接碰控件，只 PostMessage 回来，
// 由主线程调这里把它们刷成最新状态。
//
//	updateDetectUI  —— 日志框 + 自动打招呼两行 + 强制点击按钮文案
//	updateDisplay   —— 两页的点击点列表/状态 + OCR 区域描述 + 识别结果
//
// 名字里的 Detect 是历史原因（由 WM_APP_DETECT 消息驱动），独立的「人员变化检测」已删除。
package main

import "fmt"

// ---- 界面刷新 ----

// updateDetectUI 刷新日志框和「自动打招呼 / 强制点击」的状态显示。
//
// 名字里的 Detect 是历史原因（它由 WM_APP_DETECT 消息驱动），独立的「人员变化检测」
// 功能已经删除，现在它只负责：日志框 + 自动打招呼两行 + 强制点击按钮文案。
func updateDetectUI() {
	setWindowText(hwndLog, utf16ptr(logText()))
	scrollEditToEnd(hwndLog)
	updateAutoUI()
	updateForceUI()
	updateQuickRunUI()
}

func updateDisplay() {

	// 采集区两组：采集中时保留各自的「采集中…」文案，只刷新点列表
	for _, g := range []int{1, 2} {
		if !(capturing && captureGroup == g) {
			setWindowText(groupStat(g), utf16ptr(capStatusText(g)))
		}
		setWindowText(groupList(g), utf16ptr(groupPoints(g)))
	}

	// 快捷回复页的 6 组同样刷一遍（两页的控件都在，只是其中一页被隐藏着）
	updateQuickPage()

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
