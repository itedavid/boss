//go:build windows

// core_screen.go 负责屏幕截图：把屏幕上一块矩形取成 32 位 BGRA 位图交给 OCR。
package main

import (
	"errors"
	"unsafe"
)

// captureRect 截取屏幕上的一块矩形，返回自上而下的 32 位 BGRA 位图。
//
// 注意：屏幕截图本身没有 alpha 语义（BitBlt 出来的 alpha 字节是 0），
// 所以这里统一把 A 补成 255。否则 Windows OCR 会把这些像素当成全透明，
// 一张图一个字都识别不出来。
func captureRect(r Rect) (*Bitmap, error) {
	w := r.Right - r.Left
	h := r.Bottom - r.Top
	if w <= 0 || h <= 0 {
		return nil, errors.New("区域大小无效")
	}

	hdcScreen := getDC(0)
	if hdcScreen == 0 {
		return nil, errors.New("GetDC 失败")
	}
	defer releaseDC(0, hdcScreen)

	memDC := createCompatibleDC(hdcScreen)
	if memDC == 0 {
		return nil, errors.New("CreateCompatibleDC 失败")
	}
	defer deleteDC(memDC)

	bmi := BITMAPINFO{}
	bmi.Header.Size = uint32(unsafe.Sizeof(BITMAPINFOHEADER{}))
	bmi.Header.Width = int32(w)
	bmi.Header.Height = int32(-h) // 负数 = 自上而下，省得后面再翻转
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = BI_RGB

	var pbits unsafe.Pointer
	hbmp := createDIBSection(memDC, &bmi, DIB_RGB_COLORS, &pbits, 0, 0)
	if hbmp == 0 || pbits == nil {
		return nil, errors.New("CreateDIBSection 失败")
	}
	defer deleteObject(hbmp)

	old := selectObject(memDC, hbmp)
	defer selectObject(memDC, old)

	if !bitBlt(memDC, 0, 0, w, h, hdcScreen, r.Left, r.Top, SRCCOPY) {
		return nil, errors.New("BitBlt 失败")
	}

	src := unsafe.Slice((*byte)(pbits), w*h*4)
	pix := make([]byte, len(src))
	copy(pix, src)
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i+3] = 255
	}
	return &Bitmap{Pix: pix, Width: w, Height: h}, nil
}
