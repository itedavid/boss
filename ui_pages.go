//go:build windows

// ui_pages.go 负责「把界面造出来」：一次性创建两页的全部控件，并规定初始显示哪一页。
//
// 两个页面（打招呼 / 快捷回复）互不相关，各有自己的一套控件；控件只创建一次，
// 切页靠显隐（见 quick.go 的 applyPage），不重建窗口，所以句柄不泄漏、输入框内容也不丢。
package main

import "fmt"

func createControls(hwnd HWND) {
	hinst := getModuleHandle()
	font := getStockObject(DEFAULT_GUI_FONT)

	mkButton := func(text string, id int, x, y, w, h int) HWND {
		return createWindowEx(0,
			utf16ptr("BUTTON"), utf16ptr(text),
			WS_CHILD|WS_VISIBLE|BS_PUSHBUTTON,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, HMENU(id), hinst, 0)
	}
	mkLabel := func(text string, x, y, w, h int) HWND {
		return createWindowEx(0,
			utf16ptr("STATIC"), utf16ptr(text),
			WS_CHILD|WS_VISIBLE|SS_LEFT,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, 0, hinst, 0)
	}
	mkReadOnly := func(x, y, w, h int) HWND {
		return createWindowEx(WS_EX_CLIENTEDGE,
			utf16ptr("EDIT"), utf16ptr(""),
			WS_CHILD|WS_VISIBLE|WS_VSCROLL|ES_MULTILINE|ES_READONLY|ES_AUTOVSCROLL,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, 0, hinst, 0)
	}
	// 单行数字输入框：只收数字、靠右显示。
	// 传 id 是因为页面上有两个用途：打招呼次数上限、快捷回复每组的点击间隔。
	mkNumEdit := func(id int, x, y, w, h int) HWND {
		return createWindowEx(WS_EX_CLIENTEDGE,
			utf16ptr("EDIT"), utf16ptr(""),
			WS_CHILD|WS_VISIBLE|ES_AUTOHSCROLL|ES_RIGHT|ES_NUMBER,
			int32(x), int32(y), int32(w), int32(h),
			hwnd, HMENU(id), hinst, 0)
	}

	// 直接吃 rect 的版本：布局表里存的 rect 可以原样传进来，不用再手工拆 x/y/w/h。
	btnAt := func(text string, id int, r rect) HWND {
		return mkButton(text, id, r.X, r.Y, r.W, r.H)
	}
	labelAt := func(text string, r rect) HWND {
		return mkLabel(text, r.X, r.Y, r.W, r.H)
	}
	listAt := func(r rect) HWND {
		return mkReadOnly(r.X, r.Y, r.W, r.H)
	}
	numEditAt := func(id int, r rect) HWND {
		return mkNumEdit(id, r.X, r.Y, r.W, r.H)
	}

	l := controlY()
	all := []HWND{}

	// 采集区两列：左=打招呼按钮(偏移 0)，右=下一页按钮(偏移 colX 增量)。
	// rectAt 把布局表里的 rect 按列横向平移。
	colShift := func(r rect, col int) rect {
		r.X = capCellX(col)
		return r
	}

	hwndCapStat1 = labelAt("未采集", colShift(l.capTop, 0))
	hwndCapList1 = listAt(colShift(l.capBoxRow, 0))
	btnStart := btnAt("开始采集打招呼按钮", idBtnStart, colShift(l.capBtnStart, 0))
	btnStop := btnAt("停止采集打招呼按钮", idBtnStop, colShift(l.capBtnStop, 0))
	btnClear := btnAt("清空打招呼按钮", idBtnClearClks, colShift(l.capBtnClear, 0))
	hwndForceBtn1 = btnAt("强制点击打招呼", idBtnForce2, colShift(l.capBtnForce, 0))

	hwndCapStat2 = labelAt("未采集", colShift(l.capTop, 1))
	hwndCapList2 = listAt(colShift(l.capBoxRow, 1))
	btnStart2 := btnAt("开始采集下一页按钮", idBtnStart2, colShift(l.capBtnStart, 1))
	btnStop2 := btnAt("停止采集下一页按钮", idBtnStop2, colShift(l.capBtnStop, 1))
	btnClear2 := btnAt("清空下一页按钮", idBtnClearClks2, colShift(l.capBtnClear, 1))
	hwndForceBtn2 = btnAt("强制点击下一页", idBtnForce3, colShift(l.capBtnForce, 1))

	// 左列：在线状态
	hwndOnlRgn = labelAt("在线状态：未设置", colShift(l.ocrTop, 0))
	hwndOnlText = listAt(colShift(l.ocrBoxRow, 0))
	btnSelOnl := btnAt("选择在线状态", idBtnSelOnline, colShift(l.ocrBtnSel, 0))
	btnTestOnl := btnAt("测试OCR", idBtnTestOnline, colShift(l.ocrBtnTest, 0))
	btnClrOnl := btnAt("清空", idBtnClearOnl, colShift(l.ocrBtnClear, 0))
	hwndOnlStat = labelAt("在线状态：未测试", colShift(l.ocrStatRow, 0))

	// 右列：求职者姓名
	hwndNameRgn = labelAt("求职者姓名：未设置", colShift(l.ocrTop, 1))
	hwndOcrText = listAt(colShift(l.ocrBoxRow, 1))
	btnSelName := btnAt("选择求职者姓名", idBtnSelName, colShift(l.ocrBtnSel, 1))
	btnTestName := btnAt("测试OCR", idBtnTestName, colShift(l.ocrBtnTest, 1))
	btnClrName := btnAt("清空", idBtnClearName, colShift(l.ocrBtnClear, 1))
	hwndOcrStat = labelAt("求职者姓名：未测试", colShift(l.ocrStatRow, 1))

	btnClearLog := btnAt("清空日志", idBtnClearLog, l.btnLog)
	hwndLog = listAt(l.logRow)

	// 打招呼次数上限：和「开始打招呼」同一行，放在按钮左边
	lblMaxTitle := labelAt("次数上限", rect{X: colX, Y: l.btnAuto.Y + 5, W: 60, H: lblH})
	hwndMaxGreet = numEditAt(idEditMaxGreet, rect{X: colX + 64, Y: l.btnAuto.Y + 3, W: 46, H: editH})
	sendMessage(hwndMaxGreet, EM_SETLIMITTEXT, 6, 0)
	lblMaxHint := labelAt("0=不限", rect{X: colX + 312, Y: l.btnAuto.Y + 5, W: wideW - 312, H: lblH})

	btnAuto := btnAt("开始打招呼", idBtnAuto, l.btnAuto)
	btnAutoStop := btnAt("停止", idBtnAutoStop, rect{X: l.btnAuto.X + btnWRgn + 6, Y: l.btnAuto.Y, W: btnW, H: btnH})
	hwndAutoProg = labelAt("本次已打 0　本次剩余 不限", l.lblAutoProg)
	hwndAutoStat = labelAt("打招呼：未开始", l.lblAuto)

	btnExit := btnAt("退出", idBtnExit, l.exitRow)

	// 顶部一行（两页公共）：右边「窗口置顶」，紧挨着左边是页面切换按钮。
	// 这两页互不相关，但都需要置顶、也都需要能切回去，所以放在公共区而不是各页里。
	btnTop := btnAt("置顶", idBtnTop, l.topBtnRow)
	hwndPageBtn = btnAt("快捷回复", idBtnPage, l.pageBtnRow)

	// ---- 快捷回复页（B 页）控件：6 组，每组 状态标签 + 点列表 + 三个按钮 ----
	qkCells := [quickGroupCount][]HWND{}
	for i := 0; i < quickGroupCount; i++ {
		row, col := i/qkCols, i%qkCols
		dx := col * (qkColW + colGap) // 同排第几列：横向偏移
		dy := row * l.qkGroupH.H      // 第几行：纵向偏移
		at := func(r rect) rect {
			return rect{X: r.X + dx, Y: r.Y + dy, W: r.W, H: r.H}
		}
		hwndQkStat[i] = labelAt(fmt.Sprintf("第%d组 · 未采集", i+1), at(l.qkStat))
		hwndQkList[i] = listAt(at(l.qkList))
		hwndQkStart[i] = btnAt("采集", qkBtnStartBase+i, at(rect{X: l.qkBtnRow.X, Y: l.qkBtnRow.Y, W: 74, H: btnH}))
		hwndQkStop[i] = btnAt("停止", qkBtnStopBase+i, at(rect{X: l.qkBtnRow.X + 78, Y: l.qkBtnRow.Y, W: 50, H: btnH}))
		hwndQkClear[i] = btnAt("清空", qkBtnClearBase+i, at(rect{X: l.qkBtnRow.X + 132, Y: l.qkBtnRow.Y, W: 50, H: btnH}))

		// 点击间隔行：间隔 [输入框] ms
		lblGap := labelAt("间隔", at(rect{X: l.qkGapRow.X, Y: l.qkGapRow.Y + 2, W: 30, H: lblH}))
		hwndQkGap[i] = numEditAt(qkEditGapBase+i, at(rect{X: l.qkGapRow.X + 32, Y: l.qkGapRow.Y, W: 50, H: editH}))
		sendMessage(hwndQkGap[i], EM_SETLIMITTEXT, 5, 0)
		lblMs := labelAt("ms", at(rect{X: l.qkGapRow.X + 86, Y: l.qkGapRow.Y + 2, W: 24, H: lblH}))
		// 间隔多大算「慢」：这里只放一句统一说明，具体值看用户在输入框里填的数
		lblGapHint := labelAt("点到下一点等多久", at(rect{X: l.qkGapRow.X + 112, Y: l.qkGapRow.Y + 2, W: qkColW - 112, H: lblH}))

		qkCells[i] = []HWND{hwndQkStat[i], hwndQkList[i], hwndQkStart[i], hwndQkStop[i], hwndQkClear[i],
			lblGap, hwndQkGap[i], lblMs, lblGapHint}
	}

	// B 页底部公共区：轮流点击的开关键 + 说明。
	// 放在公共区（不属于任何一组）是因为它管的是「6 组一起」这件事。
	hwndQkRunBtn = btnAt("开始点击", idBtnQkRun, l.qkRunBtn)
	hwndQkRunStat = labelAt("每轮按组序 1→6，每组随机挑 1 个点，没点的组跳过，无限循环",
		l.qkRunStat)

	all = append(all, btnStart, btnStop, btnClear, hwndForceBtn1,
		btnTop, hwndPageBtn,
		btnStart2, btnStop2, btnClear2, hwndForceBtn2,
		btnSelName, btnTestName, btnClrName, btnSelOnl, btnTestOnl, btnClrOnl,
		btnClearLog, btnAuto, btnAutoStop,
		btnExit,
		hwndCapStat1, hwndCapList1, hwndCapStat2, hwndCapList2,
		hwndNameRgn, hwndOcrStat, hwndOcrText,
		hwndOnlRgn, hwndOnlStat, hwndOnlText,
		hwndLog, hwndAutoStat, hwndAutoProg,
		lblMaxTitle, hwndMaxGreet, lblMaxHint)

	// 两页的控件各归一堆，切页时整批显隐。
	// 公共区（置顶、页面切换）两边都放：置顶按钮在哪个页面都得在，切换按钮更是，
	// 所以它们不进「初始隐藏」那一批（见下面的 shared）。
	shared := []HWND{btnTop, hwndPageBtn}
	pageACtrls = append(pageACtrls, all...)
	pageACtrls = append(pageACtrls, shared...)
	pageBCtrls = append(pageBCtrls, shared...)
	pageBCtrls = append(pageBCtrls, hwndQkRunBtn, hwndQkRunStat)
	all = append(all, hwndQkRunBtn, hwndQkRunStat)
	for _, cell := range qkCells {
		pageBCtrls = append(pageBCtrls, cell...)
		all = append(all, cell...)
	}

	if font != 0 {
		for _, h := range all {
			sendMessage(h, WM_SETFONT, font, 1)
		}
	}

	// 初始状态：打招呼页可见，快捷回复页特有控件整批隐藏（与 quickPage=false 对应）。
	// 公共按钮（置顶 / 切换）初始保持可见，不参与这次隐藏。
	for _, h := range pageBCtrls {
		if h == btnTop || h == hwndPageBtn {
			continue
		}
		showWindow(h, SW_HIDE)
	}
	updatePageButton()
	updateQuickPage()
	updateQuickRunUI()
}
