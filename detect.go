//go:build windows

// detect.go 负责 V0.5 的「人员变化检测」：
//
//	不停 OCR 姓名区域 -> 和当前人员比对 -> 发现变化后先不急着换人，
//	要等同一个新结果连续出现 N 次才确认，避免 OCR 抖动（张三/张兰/张三）导致误判。
package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	detectInterval = 500 * time.Millisecond // 轮询间隔
	detectConfirm  = 3                      // 新结果连续出现几次才确认换人
	logMaxLines    = 200                    // 日志最多保留多少行
)

// 检测状态（detMu 保护）
var (
	detMu       sync.Mutex
	detRunning  bool
	detStopCh   chan struct{}
	detCurrent  string // 当前已确认的人员
	detPending  string // 待确认的新结果
	detPendingN int    // 待确认结果已连续出现的次数
	detSamples  int    // 累计识别次数
	detChanges  int    // 确认换人的次数
	detLastErr  string // 上一次的识别错误（用来避免刷屏）
)

// 日志缓冲（logMu 保护）
var (
	logMu    sync.Mutex
	logLines []string
)

// detectUIPending 是「界面刷新待处理」标记（0/1）。
//
// 后台有多个 goroutine 都会请求刷新界面（自动打招呼、检测、强制点击），
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

// startDetection 开始轮询姓名区域。
func startDetection() {
	detMu.Lock()
	if detRunning {
		detMu.Unlock()
		return
	}
	region := cfg.NameRegion
	detMu.Unlock()

	if !rectIsSet(region) {
		appendLog("开始检测失败：请先框选姓名区域")
		updateDetectUI()
		return
	}

	stop := make(chan struct{})
	detMu.Lock()
	detRunning = true
	detStopCh = stop
	detCurrent, detPending, detPendingN = "", "", 0
	detSamples, detChanges, detLastErr = 0, 0, ""
	detMu.Unlock()

	appendLog("开始检测：区域 %d,%d - %d,%d，轮询 %dms，连续 %d 次确认换人",
		region.Left, region.Top, region.Right, region.Bottom,
		detectInterval.Milliseconds(), detectConfirm)

	go detectLoop(region, stop)
	updateDetectUI()
}

// stopDetection 停止轮询。reason 会记进日志，方便看出是谁停的。
func stopDetection(reason string) {
	detMu.Lock()
	if !detRunning {
		detMu.Unlock()
		return
	}
	detRunning = false
	close(detStopCh)
	detStopCh = nil
	detMu.Unlock()

	if reason != "" {
		appendLog("停止检测（%s）", reason)
	} else {
		appendLog("停止检测")
	}
	updateDetectUI()
}

func detectionRunning() bool {
	detMu.Lock()
	defer detMu.Unlock()
	return detRunning
}

// detectLoop 是检测主循环，跑在后台 goroutine 上。
func detectLoop(region Rect, stop <-chan struct{}) {
	for {
		img, err := captureRect(region)
		switch {
		case err != nil:
			noteDetectError(err.Error())
		default:
			text, err := recognizeBitmap(img)
			if err != nil {
				noteDetectError(err.Error())
			} else {
				onNameSample(text)
				noteDetectOK()
			}
		}

		select {
		case <-stop:
			return
		case <-time.After(detectInterval):
		}
	}
}

// noteDetectError 只在错误内容变化时记一行，避免一直失败时刷屏。
func noteDetectError(msg string) {
	detMu.Lock()
	changed := msg != detLastErr
	detLastErr = msg
	detMu.Unlock()
	if changed {
		appendLog("识别失败：%s", msg)
	}
}

// noteDetectOK 从「一直失败」恢复到正常时记一行。
func noteDetectOK() {
	detMu.Lock()
	had := detLastErr != ""
	detLastErr = ""
	detMu.Unlock()
	if had {
		appendLog("识别恢复正常")
	}
}

// onNameSample 处理一次姓名识别结果，里面就是防抖的核心逻辑。
// 返回是否有「事件」发生（只用于测试）。
func onNameSample(text string) bool {
	name := normalizeName(text)

	detMu.Lock()
	detSamples++
	if name == "" {
		// OCR 没识别到字：当作「继续等待」，不动当前人员
		detMu.Unlock()
		requestDetectUI()
		return false
	}

	var events []string
	switch {
	case detCurrent == "" && detPending == "":
		// 第一次拿到有效结果，直接当作当前人员
		detCurrent = name
		events = append(events, "当前人员："+name)

	case name == detCurrent:
		// 又回到原来的人：清掉待确认状态
		if detPending != "" {
			events = append(events, "恢复为原人员："+name+"（原待确认 "+detPending+" 已丢弃）")
		}
		detPending, detPendingN = "", 0

	case name == detPending:
		detPendingN++
		if detPendingN >= detectConfirm {
			detCurrent = name
			detPending, detPendingN = "", 0
			detChanges++
			events = append(events, "检测到新人员："+name)
		} else {
			events = append(events, fmt.Sprintf("疑似换人：%s（%d/%d）", name, detPendingN, detectConfirm))
		}

	default:
		detPending = name
		detPendingN = 1
		events = append(events, fmt.Sprintf("出现新结果：%s（1/%d）", name, detectConfirm))
	}
	detMu.Unlock()

	for _, e := range events {
		appendLog("%s", e)
	}
	requestDetectUI()
	return len(events) > 0
}
