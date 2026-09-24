//go:build windows

// core_mouse.go 负责「移动鼠标 + 点击」。用 SendInput，不依赖任何第三方库。
package main

import (
	"errors"
	"math/rand"
	"time"
	"unsafe"
)

const (
	inputMouse = 0

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000

	clickMoveDelay = 40 * time.Millisecond // 移到目标后等一小会儿再按下
)

// MOUSEINPUT 必须和 Windows 原生布局一致。
type MOUSEINPUT struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// INPUT 在 x64 上 type 后面有 4 字节填充（联合体按 8 字节对齐），
// 少这个填充 SendInput 会直接报参数错误。
type INPUT struct {
	Type  uint32
	_     uint32
	Mouse MOUSEINPUT
}

var procSendInput = modUser32.NewProc("SendInput")

func sendInput(inputs []INPUT) error {
	if len(inputs) == 0 {
		return nil
	}
	r, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(INPUT{}))
	if r == 0 {
		return err
	}
	if int(r) != len(inputs) {
		return errors.New("SendInput 只生效了一部分")
	}
	return nil
}

// absoluteInput 把屏幕坐标换算成 SendInput 需要的 0..65535 绝对坐标。
// 用虚拟屏幕（多显示器）换算，和框选时记录坐标的方式一致。
func absoluteInput(x, y int, flags uint32) INPUT {
	vx := getSystemMetrics(SM_XVIRTUALSCREEN)
	vy := getSystemMetrics(SM_YVIRTUALSCREEN)
	vw := getSystemMetrics(SM_CXVIRTUALSCREEN)
	vh := getSystemMetrics(SM_CYVIRTUALSCREEN)
	if vw < 2 {
		vw = 2
	}
	if vh < 2 {
		vh = 2
	}
	return INPUT{Type: inputMouse, Mouse: MOUSEINPUT{
		Dx:      int32(int64(x-vx) * 65535 / int64(vw-1)),
		Dy:      int32(int64(y-vy) * 65535 / int64(vh-1)),
		DwFlags: flags | mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk,
	}}
}

// moveTo 把鼠标移到屏幕坐标 (x,y)，不按键。
func moveTo(x, y int) error {
	return sendInput([]INPUT{absoluteInput(x, y, 0)})
}

// leftClick 在原地按一次左键。
func leftClick() error {
	down := INPUT{Type: inputMouse, Mouse: MOUSEINPUT{DwFlags: mouseeventfLeftDown}}
	up := INPUT{Type: inputMouse, Mouse: MOUSEINPUT{DwFlags: mouseeventfLeftUp}}
	return sendInput([]INPUT{down, up})
}

// clickAt 把鼠标移到屏幕坐标 (x,y)，然后点一次左键。
func clickAt(x, y int) error {
	if err := moveTo(x, y); err != nil {
		return err
	}
	// 给系统一点时间把光标移过去，再按下/抬起；停顿也带点随机
	time.Sleep(clickMoveDelay + time.Duration(rand.Intn(70))*time.Millisecond)
	return leftClick()
}
