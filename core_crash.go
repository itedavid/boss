//go:build windows

// core_crash.go 是「崩溃黑匣子」：把原本会让程序无声闪退的 panic 兜住，
// 记成一份带完整堆栈的 crash.log（放在 exe 同目录），并弹窗提示。
//
// 为什么需要它：本程序用 -H=windowsgui 打包，没有控制台，
// 任何 goroutine / 窗口回调里的 panic 都会让进程直接消失、什么都不留，
// 也就是用户看到的「有时候闪退」。有了这里的 recover + 落盘，
//   - 后台 goroutine 的 panic 会被兜住，程序不再整个崩掉；
//   - 真的崩了也能在 crash.log 里看到是哪一行、什么原因。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// crashLogPath 返回 crash.log 的完整路径：优先放在 exe 同目录，
// 取不到就退回当前工作目录（和 configPath 的思路一致）。
func crashLogPath() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if !strings.Contains(strings.ToLower(dir), "go-build") {
			return filepath.Join(dir, "crash.log")
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "crash.log")
	}
	return "crash.log"
}

// crashMu 串行化写日志 + 弹窗，避免多个 goroutine 同时崩时互相踩。
var crashMu sync.Mutex

// crashBoxShown 保证崩溃弹窗只弹一次，避免一连串 panic 时弹窗刷屏。
var crashBoxShown bool

// recordCrash 把一次 panic 的现场（发生位置、panic 值、完整堆栈）
// 追加写进 crash.log，并（首次）弹一个提示框告诉用户日志在哪。
// where 说明是在哪个环节崩的（例如 "autoLoop"、"wndProc"）。
func recordCrash(where string, r any) {
	crashMu.Lock()
	defer crashMu.Unlock()

	stack := debug.Stack()
	ts := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf(
		"==== 崩溃记录 %s ====\n位置: %s\n原因: %v\n堆栈:\n%s\n\n",
		ts, where, r, stack)

	path := crashLogPath()
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		_, _ = f.WriteString(entry)
		_ = f.Close()
	}

	// 同时往界面日志里补一条，方便用户当场就看到「出事了」。
	appendLog("发生内部错误（%s）：%v —— 详情见 %s", where, r, path)

	if !crashBoxShown {
		crashBoxShown = true
		messageBox(0,
			fmt.Sprintf("程序遇到一个内部错误，已记录到：\n%s\n\n位置：%s\n原因：%v\n\n"+
				"程序会尽量继续运行；如果反复出现，请把 crash.log 发给开发者。",
				path, where, r),
			"Boss Helper 错误", mbIconError)
	}
}

// guard 包住一段可能 panic 的代码：一旦 panic 就记录下来并吞掉，
// 让调用它的 goroutine / 回调不至于把整个进程带崩。
// 典型用法：
//
//	go func() { defer guard("autoLoop"); autoLoop(...) }()
//
// 或在回调里：func wndProc(...) { defer guard("wndProc"); ... }
func guard(where string) {
	if r := recover(); r != nil {
		recordCrash(where, r)
	}
}

// safeGo 起一个「崩不垮全局」的后台 goroutine：函数体里 panic 会被 guard 兜住。
func safeGo(where string, fn func()) {
	go func() {
		defer guard(where)
		fn()
	}()
}
