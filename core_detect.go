//go:build windows

// core_detect.go 是「日志缓冲 + 界面刷新合并 + 姓名整理」三件事：
//
//	appendLog / logText / clearLog  —— 内存日志（最多 logMaxLines 行，只在界面日志框里显示，不落盘）
//	requestDetectUI / takeDetectUI  —— 后台 goroutine 请求主线程刷界面，合并成队列里最多一条
//	normalizeName                   —— 把 OCR 出来的姓名文本整理成可比较的字符串
//
// 文件与函数名里的 Detect 来自 WM_APP_DETECT 这条消息（它的含义就是「界面该刷了」），
// 与已删除的独立「人员变化检测」功能没有关系。
package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const logMaxLines = 200 // 日志最多保留多少行

// 日志缓冲（logMu 保护）
var (
	logMu    sync.Mutex
	logLines []string
)

// detectUIPending 是「界面刷新待处理」标记（0/1）。
//
// 后台有多个 goroutine 都会请求刷新界面（自动打招呼、强制点击），
// 如果每个请求都直接 PostMessage，主线程就要一条一条处理，
// 而单次 updateDetectUI 要重排最多 200 行日志，很贵。
// 结果就是消息队列越堆越长、主线程一直刷界面没空处理输入 —— Windows 就会显示「未响应」。
//
// 这里做「合并」：不管后台请求多少次，队列里最多只留一条 WM_APP_DETECT。
// 主线程取到消息后先把标记清零，所以在它刷界面的这段时间里攒下的新请求，
// 只会重新置标记 + 补发一条，处理完仍然是「最多一条」，不会无限堆积。
var detectUIPending int32

// requestDetectUI 请求刷新界面（后台 goroutine 调用，可重复调用）。
func requestDetectUI() {
	if atomic.CompareAndSwapInt32(&detectUIPending, 0, 1) {
		postMessage(hwndMain, WM_APP_DETECT, 0, 0)
	}
}

// takeDetectUI 主线程取走刷新请求（把标记清零，表示这次刷新已认领）。
func takeDetectUI() {
	atomic.StoreInt32(&detectUIPending, 0)
}

// appendLog 往日志里加一行（带时间戳）。
// 注意：这个函数只写数据、不刷界面；界面刷新由调用方负责
// （主线程直接刷，后台 goroutine 用 PostMessage 通知主线程）。
func appendLog(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	logMu.Lock()
	logLines = append(logLines, line)
	if len(logLines) > logMaxLines {
		logLines = logLines[len(logLines)-logMaxLines:]
	}
	logMu.Unlock()
}

func logText() string {
	logMu.Lock()
	defer logMu.Unlock()
	return strings.Join(logLines, "\r\n")
}

func clearLog() {
	logMu.Lock()
	logLines = nil
	logMu.Unlock()
}

// normalizeName 把 OCR 结果整理成用来比较的名字：
// 去掉所有空白（OCR 经常在字之间插空格），并在姓名被断成两行时把短行拼回去。
//
// 姓名区域窄的时候，OCR 偶尔会把「张三」拆成「张」「三」两行：
// 只取第一行就会丢字、导致误判换人，所以这里先把「很短的行」拼起来。
func normalizeName(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var parts []string
	for _, ln := range lines {
		s := strings.Join(strings.Fields(ln), "")
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// 单行：正常情况，直接用
	if len(parts) == 1 {
		return parts[0]
	}
	// 多行：把「每行都很短(<=2字)」的相邻行拼起来，其它情况仍只取第一行，
	// 避免把姓名下方的其它文字（如「刚刚活跃」）也拼进来。
	allShort := true
	for _, p := range parts {
		if len([]rune(p)) > 2 {
			allShort = false
			break
		}
	}
	if allShort {
		return strings.Join(parts, "")
	}
	return parts[0]
}
