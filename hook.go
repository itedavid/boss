//go:build windows

package main

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// 回调地址必须存在全局变量里保活，否则会被 GC 回收，钩子一触发就崩。
var (
	mouseHookProc = syscall.NewCallback(lowLevelMouseProc)
	kbdHookProc   = syscall.NewCallback(lowLevelKeyboardProc)
)

var (
	hookThreadID uint32
	capturing    bool // 钩子是否已安装（主线程读写）
	captureGroup int  // 当前正在采集的组：1 / 2，0 表示没有

	hotkeyHook     uintptr
	hotkeyThreadID uint32
	hotkeyRunning  bool

	clickMu     sync.Mutex
	clickQueues [2][]Point // 两组点击点队列，下标 0=打招呼按钮，1=下一页按钮
)

// startHotkey 永久安装全局键盘钩子，只为「按 Esc 紧急停止」。
// 独立线程 + 自己的消息循环；低级钩子靠安装它的线程抽消息来派发。
func startHotkey() bool {
	if hotkeyRunning {
		return true
	}
	ready := make(chan uint32, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		h := setWindowsHookEx(WH_KEYBOARD_LL, kbdHookProc, getModuleHandle(), 0)
		if h == 0 {
			ready <- 0
			return
		}
		hotkeyHook = h

		// 先摸一下消息队列，保证后面线程消息能投进来
		var m MSG
		peekMessage(&m)

		hotkeyThreadID = getCurrentThreadId()
		ready <- hotkeyThreadID

		var msg MSG
		for getMessage(&msg) != 0 {
		}

		unhookWindowsHookEx(h)
		hotkeyHook = 0
	}()

	tid := <-ready
	hotkeyRunning = tid != 0
	return hotkeyRunning
}

// startCapture 在一条独立的 OS 线程上安装全局低级鼠标钩子，并让该线程自己跑消息循环。
// 钩子回调只做两件很轻的事：把坐标压进当前组的队列、给主窗口 Post 一条消息；
// 写配置、刷界面都在主线程完成，避免阻塞。
//
// 同一时刻只允许一个组在采集（全局鼠标钩子只有一个），所以若已在采集，直接切换
// 到目标组即可；否则安装钩子并启动消息循环线程。
func startCapture(group int) bool {
	clickMu.Lock()
	if capturing {
		captureGroup = group
		clickMu.Unlock()
		return true
	}
	clickMu.Unlock()

	ready := make(chan uint32, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		h := setWindowsHookEx(WH_MOUSE_LL, mouseHookProc, getModuleHandle(), 0)
		if h == 0 {
			ready <- 0
			return
		}

		// 低级钩子靠「安装它的线程」抽消息来派发，所以先摸一下队列把消息队列建起来，
		// 这样后面 PostThreadMessage(WM_QUIT) 才不会因为线程没有队列而失败。
		var m MSG
		peekMessage(&m)

		tid := getCurrentThreadId()
		clickMu.Lock()
		hookThreadID = tid
		clickMu.Unlock()
		ready <- tid

		var msg MSG
		for getMessage(&msg) != 0 {
		}

		unhookWindowsHookEx(h)
		clickMu.Lock()
		if hookThreadID == tid {
			hookThreadID = 0
		}
		clickMu.Unlock()
	}()

	tid := <-ready
	clickMu.Lock()
	capturing = tid != 0
	if tid != 0 {
		captureGroup = group
	}
	clickMu.Unlock()
	return tid != 0
}

// stopCapture 让钩子线程退出消息循环并自行卸载钩子。
func stopCapture() {
	clickMu.Lock()
	if !capturing {
		clickMu.Unlock()
		return
	}
	capturing = false
	captureGroup = 0
	tid := hookThreadID
	clickMu.Unlock()
	if tid != 0 {
		postThreadMessage(tid, WM_QUIT, 0, 0)
	}
}

// lowLevelMouseProc 运行在钩子线程上，必须尽快返回。
func lowLevelMouseProc(nCode int32, wparam, lparam uintptr) uintptr {
	if nCode >= 0 && wparam == WM_LBUTTONDOWN {
		ms := (*MSLLHOOKSTRUCT)(uintptrToPointer(lparam))
		clickMu.Lock()
		g := captureGroup
		if g >= 1 && g <= 2 {
			clickQueues[g-1] = append(clickQueues[g-1], Point{X: int(ms.Pt.X), Y: int(ms.Pt.Y)})
		}
		clickMu.Unlock()
		if hwndMain != 0 {
			// wparam 带上当前组号，主线程据此把点并入对应组
			postMessage(hwndMain, WM_APP_CLICK, uintptr(g), 0)
		}
	}
	return callNextHookEx(nCode, wparam, lparam)
}

// lowLevelKeyboardProc 只为「按 Esc 紧急停止」，不吞按键。
func lowLevelKeyboardProc(nCode int32, wparam, lparam uintptr) uintptr {
	if nCode >= 0 && (wparam == WM_KEYDOWN || wparam == WM_SYSKEYDOWN) {
		kb := (*KBDLLHOOKSTRUCT)(uintptrToPointer(lparam))
		if kb.VkCode == VK_ESCAPE && hwndMain != 0 {
			postMessage(hwndMain, WM_APP_STOPCAP, 0, 0)
		}
	}
	return callNextHookEx(nCode, wparam, lparam)
}

// drainClicks 在主线程把某一组的队列点击点并入配置，然后刷新界面并落盘。
// group 为 1 或 2。
func drainClicks(group int) {
	if group < 1 || group > 2 {
		return
	}
	clickMu.Lock()
	pending := clickQueues[group-1]
	clickQueues[group-1] = nil
	clickMu.Unlock()

	if len(pending) == 0 {
		return
	}
	for _, p := range pending {
		// 点在我们自己窗口上的（例如 [停止采集...] 按钮）不算点击点
		if pointInMainWindow(p) {
			continue
		}
		if group == 1 {
			cfg.ClickPoints = append(cfg.ClickPoints, p)
		} else {
			cfg.ClickPoints2 = append(cfg.ClickPoints2, p)
		}
	}
	updateDisplay()
	_ = saveConfig(cfg)
}

// pointInMainWindow 判断某个屏幕坐标是否落在主窗口上。
func pointInMainWindow(p Point) bool {
	if hwndMain == 0 {
		return false
	}
	var r RECT
	if !getWindowRect(hwndMain, &r) {
		return false
	}
	return p.X >= int(r.Left) && p.X < int(r.Right) && p.Y >= int(r.Top) && p.Y < int(r.Bottom)
}

// uintptrToPointer 把回调参数里的 uintptr 还原成指针。
// 低级钩子的 lParam 本来就是指向结构体的指针，这是标准的 FFI 用法；
// 直接写 unsafe.Pointer(lparam) 会被 go vet 当成「uintptr 转指针」误报，所以绕一层取值。
func uintptrToPointer(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}
