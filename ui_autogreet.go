//go:build windows

// ui_autogreet.go 是「自动打招呼」的界面侧参数：次数上限输入框 ↔ 配置 ↔ 后台循环。
//
// 输入框里的数字改动即时生效——后台循环每次要打招呼前都会重新读一次，
// 所以运行中调小也能立刻拦住，不用停止再来。
package main

import (
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// 程序化写入输入框时用它挡掉 EN_CHANGE 的自反馈。
//
// 为什么需要：loadMaxGreetsToUI / loadQuickIntervalsToUI 会用 setWindowText 往
// 输入框里填配置值，而这一步会立刻触发 EN_CHANGE；处理 EN_CHANGE 的代码又会
// 用 parseEditInt 把框里的内容读回配置——但此刻文本还没真正提交，读回来是空，
// 于是刚填进去的值被当成 0 写回去（配置被清掉、框也变空）。
// 用一个计数标志把这段自反馈期圈出来，EN_CHANGE 期间直接跳过同步。
var (
	uiLoadingMu sync.Mutex
	uiLoading   int
)

// beginUIProgrammaticWrite / endUIProgrammaticWrite 圈住「程序在往控件里写值」这段时间。
// 用计数而不是 bool：将来嵌套调用也不会互相踩。
func beginUIProgrammaticWrite() {
	uiLoadingMu.Lock()
	uiLoading++
	uiLoadingMu.Unlock()
}

func endUIProgrammaticWrite() {
	uiLoadingMu.Lock()
	if uiLoading > 0 {
		uiLoading--
	}
	uiLoadingMu.Unlock()
}

// uiProgrammaticWrite 返回当前是否处在「程序写控件值」期间。
func uiProgrammaticWrite() bool {
	uiLoadingMu.Lock()
	defer uiLoadingMu.Unlock()
	return uiLoading > 0
}

// setEditInt 往数字输入框里写一个整数，并挡掉由此触发的 EN_CHANGE 自反馈。
func setEditInt(hwnd HWND, n int) {
	if hwnd == 0 {
		return
	}
	beginUIProgrammaticWrite()
	setWindowText(hwnd, utf16ptr(strconv.Itoa(n)))
	endUIProgrammaticWrite()
}

// parseEditInt 读输入框里的非负整数；空着或读不出数字都按 0（不限）处理。
func parseEditInt(hwnd HWND) int {
	if hwnd == 0 {
		return 0
	}
	buf := make([]uint16, 24)
	n := getWindowText(hwnd, &buf[0], len(buf))
	if n <= 0 {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(syscall.UTF16ToString(buf[:n])))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// loadMaxGreetsToUI 启动时把配置里的次数上限填进输入框。
func loadMaxGreetsToUI() {
	if cfg.MaxGreets > 0 {
		setEditInt(hwndMaxGreet, cfg.MaxGreets)
	}
	setAutoMaxGreets(cfg.MaxGreets)
}

// syncMaxGreetsFromUI 把输入框里的次数上限同步到内存配置并落盘。
// 改动即时生效：后台循环每次要打招呼前都会重新读一次，运行中调小也能立刻拦住。
//
// 回填期间（uiProgrammaticWrite）直接返回，避免把刚填进去的值读成 0 又写回配置。
func syncMaxGreetsFromUI() {
	if uiProgrammaticWrite() {
		return
	}
	n := parseEditInt(hwndMaxGreet)
	setAutoMaxGreets(n)
	if n != cfg.MaxGreets {
		cfg.MaxGreets = n
		_ = saveConfig(cfg)
	}
	requestDetectUI() // 让「已打招呼 X / 上限 Y」跟着刷新
}
