# boss

Boss Helper —— 一个纯 Go 写的 Windows 桌面小工具，零第三方依赖，编译出来只有一个轻量 exe。

## 功能
- **区域框选**：全屏半透明遮罩上拖拽框选，保存屏幕矩形坐标到 `config.json`（DPI 感知，支持多显示器）
- **点击点采集**：全局低级鼠标钩子，记录屏幕上的点击坐标
- **本地 OCR**：直接调用 Windows 内置 WinRT OCR，识别中文姓名 / 在线状态，无需 Python / Node / 外部引擎
- **人员变化检测**：带防抖，避免 OCR 抖动误触发
- **自动打招呼 / 强制点击**：后台 goroutine 驱动，主线程只负责画界面

## 构建
需要 Go 1.21+，仅支持 Windows（用了 Win32 API）。

```bat
go build -o BossHelper.exe -ldflags "-H windowsgui -s -w" .
```

或用仓库里的 `build.cmd`。

## 运行
直接双击 `BossHelper.exe`。首次运行先在界面上框选「求职者姓名区」「在线状态区」，并采集好点击点（写入 `config.json`），之后才能使用 OCR / 检测 / 自动功能。按 `Esc` 可紧急停止采集。

## 说明
- 模块：`bosshelper`，`go.mod` 无任何 `require`
- 窗口 / 消息循环 / GDI / 钩子 / COM 全部用标准库 `syscall` + `unsafe` 直连 Win32
