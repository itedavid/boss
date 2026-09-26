//go:build windows

// ui_layout.go 是「窗口布局计算」：所有控件的坐标与大小都从这里算出来，创建控件时按表取值。
//
// 布局分两页（打招呼 / 快捷回复），但同一时刻只显示一页，所以两套坐标各自从 topY 起算，
// 窗口高度取较高的那一页。切页只做显隐，不重算布局。
package main

// 统一的间距常量：消灭 "y += btnH + 4" 里那些散落的魔法数字。
const (
	rowGap    = 4  // 常规行间距（相邻控件）
	rowGapBig = 12 // 区块之间的额外留白
	gapTight  = 2  // 紧凑行距（同一逻辑块内的相邻行）
	gapLoose  = 6  // 略宽行距（日志区上方等）
)

// rect 是一个控件的完整位置与大小。
// 把四个值打包成一个结构，省得 x / y / w / h 各写各的、容易串位。
type rect struct{ X, Y, W, H int }

// xywh 把 rect 拆成 createWindowEx 需要的四个参数。
func (r rect) xywh() (int32, int32, int32, int32) {
	return int32(r.X), int32(r.Y), int32(r.W), int32(r.H)
}

// rowCursor 是自上而下的布局游标：
// 每调用一次 next，就「取走当前行的 y」并把游标往下推（控件高 + 间距）。
type rowCursor struct{ y int }

// next 返回当前行的 y，并把游标推进 h + gap。
func (c *rowCursor) next(h, gap int) int {
	y := c.y
	c.y += h + gap
	return y
}

// capCellX 返回采集区第 col 列（0 起）的 x 坐标。
// 目前界面按两列排：0=左列、1=右列，与原来的 ocrLeftX / ocrRightX 等价。
func capCellX(col int) int {
	if col == 0 {
		return ocrLeftX
	}
	return ocrRightX
}

// layout 记录各控件的位置与大小。控件按顺序排，保证互不重叠。
type layout struct {
	capTop, capBoxRow                                 rect
	capBtnStart, capBtnStop, capBtnClear, capBtnForce rect
	capBottom                                         rect

	// 两个 OCR 区域并排：左=在线状态，右=求职者姓名（两列共用同一套 y）
	ocrTop, ocrBoxRow                  rect
	ocrBtnSel, ocrBtnTest, ocrBtnClear rect
	ocrStatRow, ocrBottom              rect

	btnLog, logRow       rect
	btnAuto, lblAutoProg rect
	lblAuto, exitRow     rect
	topBtnRow            rect // 顶部「窗口置顶」按钮所在行

	// ---- 快捷回复页（B 页）----
	// 每组控件同理：存「第 1 组」的 rect，第 n 组按 qkCols 换行偏移（共 8 组、四行）。
	qkTop        rect // 该页标题/说明行
	qkStat       rect // 每组的状态标签
	qkList       rect // 每组的点列表
	qkBtnRow     rect // 每组的三个按钮所在行（采集/停止/清空）
	qkGapRow     rect // 每组的点击间隔输入行（间隔 [ 500 ] ms）
	qkGroupH     rect // 每组占的总高（用 H 记高度）
	qkBottom     rect
	qkRunBtn     rect // B 页底部「开始点击」按钮（公共区）
	qkRunStat    rect // 该按钮右边的一行说明
	qkBatchStat  rect // 运行状态行（本批进度 / 累计轮数 / 批间休息）
	qkPauseLbl1  rect // 批间休息设置：前缀「批间休息」
	qkPauseMin   rect // 批间休息设置：最小分钟输入框
	qkPauseTilde rect // 批间休息设置：「~」
	qkPauseMax   rect // 批间休息设置：最大分钟输入框
	qkPauseLbl2  rect // 批间休息设置：后缀「分钟（0/留空=不休息）」
	qkPageBottom int  // B 页最底部 y（窗口高度估算用）
	pageBtnRow   rect // 页面切换按钮所在行（公共区，与置顶同一行）
	bPage        int  // B 页内容起始 y（仅用于窗口高度估算）
}

func controlY() layout {
	var l layout
	c := &rowCursor{y: topY}
	// 顶部一行：右侧「置顶」按钮，其左边是「快捷回复 / 返回」页面切换按钮（两页公共）
	topRowY := c.next(btnH, rowGap)
	l.topBtnRow = rect{X: colX + wideW - 140, Y: topRowY, W: 140, H: btnH}
	l.pageBtnRow = rect{X: colX + wideW - 140 - 8 - 110, Y: topRowY, W: 110, H: btnH}
	// 采集区：打招呼按钮 / 下一页按钮 两列并排（宽度是单列宽，创建时按列再平移 x）
	l.capTop = rect{X: colX, Y: c.next(lblH, rowGap), W: ocrColW, H: lblH}
	l.capBoxRow = rect{X: colX, Y: c.next(listH, rowGap), W: ocrColW, H: listH}
	l.capBtnStart = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.capBtnStop = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.capBtnClear = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.capBtnForce = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.capBottom = rect{Y: c.next(0, 0)}

	// 两个 OCR 区域并排（左右两列共用同一套 y；宽度同为单列宽）
	l.ocrTop = rect{X: colX, Y: c.next(lblH, rowGap), W: ocrColW, H: lblH}
	l.ocrBoxRow = rect{X: colX, Y: c.next(rltH, rowGap), W: ocrColW, H: rltH}
	l.ocrBtnSel = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.ocrBtnTest = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.ocrBtnClear = rect{X: colX, Y: c.next(btnH, rowGap), W: ocrColW, H: btnH}
	l.ocrStatRow = rect{X: colX, Y: c.next(lblH, rowGapBig), W: ocrColW, H: lblH}
	l.ocrBottom = rect{Y: c.y}

	l.btnLog = rect{X: colX, Y: c.next(btnH, gapLoose), W: btnW, H: btnH}
	l.logRow = rect{X: colX, Y: c.next(logH, rowGapBig), W: wideW, H: logH}

	// 「次数上限」输入框和「开始打招呼」「停止」共用这一行（控件见 createControls）
	l.btnAuto = rect{X: colX + 116, Y: c.next(btnH, gapTight), W: btnWRgn, H: btnH}
	// 进度单独占一行（本次已打 / 本次剩余），比塞在状态标签里更好认
	l.lblAutoProg = rect{X: colX, Y: c.next(lblH, gapTight), W: wideW, H: lblH}
	l.lblAuto = rect{X: colX, Y: c.next(lblH, rowGapBig-gapTight), W: wideW, H: lblH}

	l.exitRow = rect{X: colX, Y: c.y, W: btnW, H: btnH}
	l.bPage = c.y // B 页从这里开始

	// ---- 快捷回复页（B 页）布局 ----
	// 它从同一行顶部开始（与 A 页并排在同一窗口里，靠显隐切换），本不必接在 A 页下方；
	// 但为简单起见，B 页的 y 也从头算一份，窗口高度取两页较大者。
	cb := &rowCursor{y: topY}
	// B 页顶部说明行
	l.qkTop = rect{X: colX, Y: cb.next(lblH, rowGap), W: wideW, H: lblH}
	// 8 组：每行 qkCols 组，共四行。每组 = 状态标签 + 点列表 + 按钮行 + 间隔行
	l.qkStat = rect{X: colX, Y: cb.next(lblH, rowGap), W: qkColW, H: lblH}
	l.qkList = rect{X: colX, Y: cb.next(qkListH, rowGap), W: qkColW, H: qkListH}
	l.qkBtnRow = rect{X: colX, Y: cb.next(btnH, rowGap), W: qkColW, H: btnH}
	l.qkGapRow = rect{X: colX, Y: cb.next(editH, rowGap), W: qkColW, H: editH}
	groupH := cb.y - l.qkStat.Y // 一组占的总高
	l.qkGroupH = rect{H: groupH}
	rows := (quickGroupCount + qkCols - 1) / qkCols
	l.qkBottom = rect{Y: l.qkStat.Y + rows*groupH}
	// 底部公共区：轮流点击的 [开始点击] 按钮 + 一行说明（不属于任何一组）
	l.qkRunBtn = rect{X: colX, Y: l.qkBottom.Y + rowGapBig, W: btnW, H: btnH}
	l.qkRunStat = rect{X: colX + btnW + gapLoose, Y: l.qkRunBtn.Y + 5, W: wideW - btnW - gapLoose, H: lblH}
	// 运行状态行：本批进度 / 累计轮数 / 批间休息（单独占一行，便于放下长文案）
	l.qkBatchStat = rect{X: colX, Y: l.qkRunBtn.Y + btnH + rowGap, W: wideW, H: lblH}
	// 批间休息设置行：批间休息 [Min] ~ [Max] 分钟（0/留空=不休息）
	pauseY := l.qkBatchStat.Y + lblH + rowGap
	px := colX
	l.qkPauseLbl1 = rect{X: px, Y: pauseY + 2, W: 60, H: lblH}
	l.qkPauseMin = rect{X: px + 64, Y: pauseY, W: 52, H: editH}
	l.qkPauseTilde = rect{X: px + 120, Y: pauseY + 2, W: 12, H: lblH}
	l.qkPauseMax = rect{X: px + 134, Y: pauseY, W: 52, H: editH}
	l.qkPauseLbl2 = rect{X: px + 192, Y: pauseY + 2, W: wideW - 192, H: lblH}
	l.qkPageBottom = pauseY + editH
	return l
}

// clientSize 算出刚好放得下所有控件的客户区大小。
// 两页共用同一个窗口，取两页里较高的那一页（8 组的快捷回复页可能比打招呼页更高）。
func clientSize() (int, int) {
	l := controlY()
	aBottom := l.exitRow.Y + btnH
	bBottom := l.qkPageBottom
	bottom := aBottom
	if bBottom > bottom {
		bottom = bBottom
	}
	return colX + wideW + colX, bottom + topY
}
