//go:build windows

// ocr_ui.go 是 OCR 的界面侧：区域框选与保存、发起识别、把识别结果刷到状态标签和结果框。
//
// 识别本身在 ocr_run.go / ocr_engine.go 里；这里只管「界面上的这块区域」这一层：
// 哪个 kind 对应哪个区域、哪两个控件，以及识别是走后台 goroutine + PostMessage 回主线程。
package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- OCR 区域（姓名 / 在线状态）----

// regionOf 取某个 OCR 任务对应的区域。
func regionOf(kind int) Rect {
	if kind == ocrKindName {
		return cfg.NameRegion
	}
	return cfg.OnlineRegion
}

// setRegion 写回某个 OCR 任务对应的区域。
func setRegion(kind int, r Rect) {
	if kind == ocrKindName {
		cfg.NameRegion = r
	} else {
		cfg.OnlineRegion = r
	}
}

// selectOCRRegion 框选一块 OCR 区域并保存。
func selectOCRRegion(kind int) {
	r, ok := selectRegion(hwndMain)
	showWindow(hwndMain, SW_SHOW)
	setForegroundWindow(hwndMain)
	if !ok {
		return
	}
	setRegion(kind, r)
	appendLog("%s设置完成：%d,%d - %d,%d", kindLabel(kind),
		r.Left, r.Top, r.Right, r.Bottom)
	updateDisplay()
	_ = saveConfig(cfg)
}

// clearOCRRegion 清空某块区域和它的识别结果。
func clearOCRRegion(kind int) {
	setRegion(kind, Rect{})
	ocrMu.Lock()
	if kind == ocrKindName {
		ocrName = ocrState{}
	} else {
		ocrOnline = ocrState{}
	}
	ocrMu.Unlock()
	appendLog("已清空%s", kindLabel(kind))
	updateDisplay()
	_ = saveConfig(cfg)
}

// runOCRRegion 截图 + 识别。截图和识别都放后台 goroutine，完成后 PostMessage 回主线程。
func runOCRRegion(kind int, region Rect) {
	if ocrBusy {
		return
	}
	if !rectIsSet(region) {
		setWindowText(statLabel(kind), utf16ptr("请先框选区域"))
		return
	}

	ocrBusy = true
	setWindowText(statLabel(kind), utf16ptr(kindLabel(kind)+"：识别中…"))

	safeGo("runOCRRegion", func() {
		// 万一这个 goroutine 崩了，也要把 ocrBusy 放开，否则「测试OCR」按钮会一直点不动。
		defer func() {
			if r := recover(); r != nil {
				ocrBusy = false
				recordCrash("runOCRRegion", r)
			}
		}()
		st := ocrState{tested: true}
		img, err := captureRect(region)
		if err != nil {
			st.err = err
		} else {
			start := time.Now()
			st.text, st.err = recognizeBitmap(img)
			st.elapsed = time.Since(start)
		}

		ocrMu.Lock()
		if kind == ocrKindName {
			ocrName = st
		} else {
			ocrOnline = st
		}
		ocrMu.Unlock()

		postMessage(hwndMain, WM_APP_OCR, uintptr(kind), 0)
	})
}

// onOCRReady 在主线程把后台识别结果刷到界面上。
func onOCRReady(kind int) {
	ocrBusy = false
	updateOCRDisplay(kind)

	ocrMu.Lock()
	st := ocrName
	if kind == ocrKindOnline {
		st = ocrOnline
	}
	ocrMu.Unlock()

	if st.err != nil {
		appendLog("%s失败：%s", kindLabel(kind), st.err)
	} else if kind == ocrKindOnline {
		appendLog("在线状态 OCR：%q → %s", st.text, onlineVerdict(st.text))
	} else {
		appendLog("求职者姓名 OCR：%q（%dms）", st.text, st.elapsed.Milliseconds())
	}
	updateDetectUI()
}

// ---- 识别结果的显示 ----

func kindLabel(kind int) string {
	if kind == ocrKindName {
		return "求职者姓名"
	}
	return "在线状态"
}

func statLabel(kind int) HWND {
	if kind == ocrKindName {
		return hwndOcrStat
	}
	return hwndOnlStat
}

func textBox(kind int) HWND {
	if kind == ocrKindName {
		return hwndOcrText
	}
	return hwndOnlText
}

// firstLine 取多行文本的第一行，用于状态栏显示。
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// onlineVerdict 把在线区域的 OCR 文本判成「在线 / 不在线」。
func onlineVerdict(text string) string {
	if strings.TrimSpace(text) == "" {
		return "不在线"
	}
	if looksOnline(text) {
		return "在线"
	}
	return "不在线"
}

// updateOCRDisplay 按任务类型刷新对应的状态标签和结果框。
func updateOCRDisplay(kind int) {
	ocrMu.Lock()
	st := ocrName
	if kind == ocrKindOnline {
		st = ocrOnline
	}
	ocrMu.Unlock()

	stat := statLabel(kind)
	box := textBox(kind)

	if !st.tested {
		if kind == ocrKindName {
			setWindowText(stat, utf16ptr("求职者姓名：未测试"))
		} else {
			setWindowText(stat, utf16ptr("在线状态：未测试"))
		}
		setWindowText(box, utf16ptr(""))
		return
	}

	if st.err != nil {
		setWindowText(stat, utf16ptr(kindLabel(kind)+"：失败（"+st.err.Error()+"）"))
		setWindowText(box, utf16ptr(""))
		return
	}

	if strings.TrimSpace(st.text) == "" {
		msg := fmt.Sprintf("%s：没识别到文字（%dms）", kindLabel(kind), st.elapsed.Milliseconds())
		if kind == ocrKindOnline {
			msg = fmt.Sprintf("在线状态：不在线（没识别到文字，%dms）", st.elapsed.Milliseconds())
		}
		setWindowText(stat, utf16ptr(msg))
		setWindowText(box, utf16ptr(""))
		return
	}

	if kind == ocrKindOnline {
		setWindowText(stat, utf16ptr(fmt.Sprintf("在线状态：%s（%dms）",
			onlineVerdict(st.text), st.elapsed.Milliseconds())))
	} else {
		setWindowText(stat, utf16ptr(fmt.Sprintf("求职者姓名：%s（%dms）",
			firstLine(st.text), st.elapsed.Milliseconds())))
	}
	setWindowText(box, utf16ptr(st.text))
}

// ---- 识别结果的状态（后台 goroutine ↔ 主线程）----

// ocrState 是一次识别的结果。
type ocrState struct {
	text    string
	elapsed time.Duration
	err     error
	tested  bool
}

// OCR 结果从后台 goroutine 交回主线程。
var (
	ocrMu     sync.Mutex
	ocrName   ocrState
	ocrOnline ocrState
	ocrBusy   bool
)
