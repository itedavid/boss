//go:build windows

// core_winapi_hook.go 补充 V0.2 用到的 Win32 声明：
// 全局低级钩子、窗口矩形、线程消息投递。仍然只用 Go 标准库 syscall，无第三方依赖。

package main

import "unsafe"

const (
	WM_APP          = 0x8000
	WM_APP_CLICK    = WM_APP + 1 // 钩子线程 -> 主线程：采集到一个点击点
	WM_APP_STOPCAP  = WM_APP + 2 // 钩子线程 -> 主线程：用户按了 Esc
	WM_APP_OCR      = WM_APP + 3 // OCR 后台 goroutine -> 主线程：识别完成
	WM_APP_DETECT   = WM_APP + 4 // 后台 goroutine -> 主线程：状态有变化，刷新界面
	WM_APP_STOPAUTO = WM_APP + 5 // 钩子线程 -> 主线程：用户按了 Ctrl+C，停自动化（自动打招呼 + 强制点击），不动采集
	WM_APP_LOADCFG  = WM_APP + 6 // 主线程自投：窗口创建完成后再把配置回填到输入框

	WM_SYSKEYDOWN = 0x0104

	WH_KEYBOARD_LL = 13
	WH_MOUSE_LL    = 14

	PM_NOREMOVE = 0x0000
)

// RECT 是屏幕坐标矩形。
type RECT struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// MSLLHOOKSTRUCT 是 WH_MOUSE_LL 回调 lParam 指向的结构。
type MSLLHOOKSTRUCT struct {
	Pt          POINT
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// KBDLLHOOKSTRUCT 是 WH_KEYBOARD_LL 回调 lParam 指向的结构。
type KBDLLHOOKSTRUCT struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

var (
	procSetWindowsHookExW   = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = modUser32.NewProc("CallNextHookEx")
	procGetWindowRect       = modUser32.NewProc("GetWindowRect")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procPostThreadMessageW  = modUser32.NewProc("PostThreadMessageW")
	procPeekMessageW        = modUser32.NewProc("PeekMessageW")

	procGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")
)

// setWindowsHookEx 安装钩子；threadID 为 0 表示全局钩子。
func setWindowsHookEx(idHook int, lpfn uintptr, hmod HINSTANCE, threadID uint32) uintptr {
	r, _, _ := procSetWindowsHookExW.Call(uintptr(idHook), lpfn, uintptr(hmod), uintptr(threadID))
	return r
}

func unhookWindowsHookEx(hook uintptr) bool {
	r, _, _ := procUnhookWindowsHookEx.Call(hook)
	return r != 0
}

// callNextHookEx 把事件交给钩子链上的下一个钩子。nCode 可能为负，转 uintptr 时会自动符号扩展。
func callNextHookEx(nCode int32, wparam, lparam uintptr) uintptr {
	r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wparam, lparam)
	return r
}

func getCurrentThreadId() uint32 {
	r, _, _ := procGetCurrentThreadId.Call()
	return uint32(r)
}

func getWindowRect(hwnd HWND, r *RECT) bool {
	ret, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(r)))
	return ret != 0
}

func postMessage(hwnd HWND, msg uint32, wparam, lparam uintptr) bool {
	r, _, _ := procPostMessageW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r != 0
}

func postThreadMessage(threadID uint32, msg uint32, wparam, lparam uintptr) bool {
	r, _, _ := procPostThreadMessageW.Call(uintptr(threadID), uintptr(msg), wparam, lparam)
	return r != 0
}

// peekMessage 只看一眼队列里的消息，不取走；用来强制当前线程建立消息队列。
func peekMessage(msg *MSG) bool {
	r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(msg)), 0, 0, 0, PM_NOREMOVE)
	return r != 0
}
