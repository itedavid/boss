//go:build windows

// ocr.go 定义 OCR 的通用接口。以后要换引擎（例如 ONNX），
// 只要再写一个实现了这个接口的类型即可，其它代码不用动。
package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

// Bitmap 是一张 32 位 BGRA 位图：每像素 4 字节（B,G,R,A），自上而下排列。
type Bitmap struct {
	Pix    []byte
	Width  int
	Height int
}

// OCR 是文字识别接口。
//
// 说明：需求里给的示例签名是 Recognize(image []byte)，
// 但纯字节数组没法自带宽高，所以这里用 Bitmap 带上尺寸信息。
type OCR interface {
	Recognize(img *Bitmap) (string, error)
}

// looksOnline 判断 OCR 出来的在线状态文本是不是「在线」。
//
// OCR 有时会在字与字之间插空格（例如「在 线」），所以先把空白都去掉再比。
// 另外「不在线」里面也包含「在线」，必须单独排除掉，不然会误判。
func looksOnline(text string) bool {
	flat := strings.Join(strings.Fields(text), "")
	if strings.Contains(flat, "不在线") {
		return false
	}
	return strings.Contains(flat, "在线")
}

// scoreGood 是「这个结果已经够像样了」的分数线，够到了就不用再试别的候选图。
const scoreGood = 6

// scoreText 给一次 OCR 结果打分，用来在几张候选图里挑最好的那个结果。
//
// 汉字最值钱，字母数字次之，空白不算分，其它古怪符号倒扣分：
// 这样「张三」会打败「张 三1」，也打败纯符号的噪声结果。
//
// 在此之上再加一层「像不像我们真正要读的东西」的偏好：姓名/在线状态都是
// 纯中文短词，所以纯汉字、长度合理的结果额外加分，混了数字或符号的结果减分，
// 帮着在两张都认出一部分字的候选里挑出更干净的那个。
func scoreText(s string) int {
	score := 0
	han, other := 0, 0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			score += 3
			han++
		case unicode.IsLetter(r), unicode.IsDigit(r):
			score += 2
			other++
		case unicode.IsSpace(r):
			// 空白不加不减
		default:
			score--
			other++
		}
	}
	// 纯中文短词（2~6 字）最贴合姓名/在线状态，给一层形状加分
	switch {
	case han >= 2 && han <= 6 && other == 0:
		score += 2
	case han > 0 && other == 0:
		score++
	}
	// 夹带了非中文内容，说明这张候选图没有上一张干净
	if other > 0 {
		score -= other
	}
	return score
}

// ---- OCR 任务队列：引擎必须固定在一条线程上跑 ----

type ocrJob struct {
	img  *Bitmap
	text string
	err  error
	done chan struct{}
}

var (
	ocrJobs     = make(chan *ocrJob)
	ocrWorkerMu sync.Mutex
	ocrStarted  bool
)

// recognizeBitmap 把截图交给 OCR 线程；调用方可以是任意 goroutine。
func recognizeBitmap(img *Bitmap) (string, error) {
	ocrWorkerMu.Lock()
	if !ocrStarted {
		ocrStarted = true
		go ocrLoop()
	}
	ocrWorkerMu.Unlock()

	job := &ocrJob{img: img, done: make(chan struct{})}
	ocrJobs <- job
	<-job.done
	return job.text, job.err
}

// ocrLoop 是常驻的 OCR 线程。
// WinRT/COM 有「线程套间」的概念，引擎在同一线程上创建和使用最稳妥，
// 所以这里锁一条 OS 线程，引擎只在这里创建和调用。
func ocrLoop() {
	// OCR 线程整体也兜一层：万一连 recover 都兜不住（极少见），至少留下崩溃记录。
	defer guard("ocrLoop")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var eng OCR
	var initErr error
	for job := range ocrJobs {
		if eng == nil && initErr == nil {
			e, err := newWinOCREngine()
			if err != nil {
				initErr = err
			} else {
				eng = e
			}
		}
		// 单个任务单独兜 panic：一张有问题的截图不该崩掉整条 OCR 线程，
		// 更不能让调用方在 <-job.done 上永远卡住（那会表现成界面卡死）。
		runOCRJob(eng, initErr, job)
	}
}

// runOCRJob 处理一个 OCR 任务，保证无论如何 job.done 都会被关闭。
func runOCRJob(eng OCR, initErr error, job *ocrJob) {
	defer close(job.done)
	defer func() {
		if r := recover(); r != nil {
			recordCrash("ocrJob", r)
			job.err = fmt.Errorf("OCR 内部错误：%v", r)
		}
	}()
	if initErr != nil {
		job.err = initErr
		return
	}
	job.text, job.err = eng.Recognize(job.img)
}
