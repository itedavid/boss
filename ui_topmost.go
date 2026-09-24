//go:build windows

// ui_topmost.go 管「窗口置顶」这一件事：把 topMostOn 落到窗口层级和按钮文案上。
//
// 置顶按钮在顶部一行，属于两页公共区（打招呼页、快捷回复页都能看到同一个按钮），
// 所以状态是全局的：任一页切换，另一页看到的文案也是同步的。
package main

// applyTopMost 把 topMostOn 的当前值同步到窗口层级与按钮标题。
func applyTopMost() {
	if hwndMain == 0 {
		return
	}
	after := hwndNoTopMost
	caption := "置顶"
	if topMostOn {
		after = hwndTopMost
		caption = "已置顶·点此取消"
	}
	setWindowPos(hwndMain, after, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
	if hwndTopBtn != 0 {
		setWindowText(hwndTopBtn, utf16ptr(caption))
	}
}

// toggleTopMost 点击右上角按钮时切换置顶，并写入配置（下次启动保持）。
func toggleTopMost() {
	topMostOn = !topMostOn
	applyTopMost()
	cfg.TopMost = topMostOn
	_ = saveConfig(cfg)
}
