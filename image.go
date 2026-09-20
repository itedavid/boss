//go:build windows

// image.go 负责交给 OCR 之前的图像预处理，全是纯 Go 的像素运算，方便单独测试：
//
//	裁掉四周多余的背景 -> 按文字实际高度放大 -> 深色底自动转成浅色底 -> 补一圈边
//
// 除了原图，还会准备「二值化」「反色」两个候选项，供识别阶段兜底。
package main

import "math"

const (
	inkPadPx     = 3    // 裁到文字后四周留的空
	padPx        = 8    // 放大后补的边，字贴着边 OCR 容易漏
	ocrTargetH   = 40   // 希望文字放大到的高度（Windows OCR 对 30~60 像素最擅长）
	ocrMaxScale  = 6    // 最多放大倍数
	inkThreshold = 48   // 和背景的色差超过多少算「有笔迹」
	inkAnyThr    = 24   // 比 inkThreshold 松，只用来判断「这块区域里到底有没有字」
	inkMinPixels = 6    // 笔迹像素少于这个数就当作没有字
	inkMaxRatio  = 0.92 // 笔迹框占整张图的比例超过它就说明背景不干净，别裁
	darkBgLum    = 110  // 背景亮度低于这个值就认为这是深色底，自动反色
)

// subImage 抠出 [x0,y0)-(x1,y1) 这块，返回新位图（alpha 统一 255）。
func subImage(img *Bitmap, x0, y0, x1, y1 int) *Bitmap {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > img.Width {
		x1 = img.Width
	}
	if y1 > img.Height {
		y1 = img.Height
	}
	w, h := x1-x0, y1-y0
	dst := &Bitmap{Pix: make([]byte, w*h*4), Width: w, Height: h}
	for y := 0; y < h; y++ {
		src := ((y0+y)*img.Width + x0) * 4
		copy(dst.Pix[y*w*4:(y+1)*w*4], img.Pix[src:src+w*4])
	}
	for i := 3; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = 255
	}
	return dst
}

// packRGB 把 BGRA 里的 BGR 打包成一个整数，方便比较颜色。
func packRGB(p []byte, i int) uint32 {
	return uint32(p[i+2])<<16 | uint32(p[i+1])<<8 | uint32(p[i])
}

// rgbDiff 两个颜色的最大单通道差，用来判断「这个像素和背景像不像」。
func rgbDiff(a, b uint32) int {
	dr := int(a>>16&0xff) - int(b>>16&0xff)
	dg := int(a>>8&0xff) - int(b>>8&0xff)
	db := int(a&0xff) - int(b&0xff)
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	if dg > dr {
		dr = dg
	}
	if db > dr {
		dr = db
	}
	return dr
}

// borderColor 取四周一圈像素里出现最多的颜色，作为这块区域的背景色。
func borderColor(img *Bitmap) uint32 {
	counts := map[uint32]int{}
	add := func(x, y int) {
		counts[packRGB(img.Pix, (y*img.Width+x)*4)]++
	}
	for x := 0; x < img.Width; x++ {
		add(x, 0)
		add(x, img.Height-1)
	}
	for y := 0; y < img.Height; y++ {
		add(0, y)
		add(img.Width-1, y)
	}
	var best uint32
	bestN := -1
	for c, n := range counts {
		if n > bestN {
			best, bestN = c, n
		}
	}
	return best
}

// inkBox 找出和背景色不一样的像素范围，返回右下角（不含）坐标和笔迹像素数。
func inkBox(img *Bitmap, bg uint32, thr int) (x0, y0, x1, y1, n int) {
	x0, y0 = img.Width, img.Height
	x1, y1 = -1, -1
	for y := 0; y < img.Height; y++ {
		for x := 0; x < img.Width; x++ {
			if rgbDiff(packRGB(img.Pix, (y*img.Width+x)*4), bg) > thr {
				n++
				if x < x0 {
					x0 = x
				}
				if y < y0 {
					y0 = y
				}
				if x >= x1 {
					x1 = x + 1
				}
				if y >= y1 {
					y1 = y + 1
				}
			}
		}
	}
	return x0, y0, x1, y1, n
}

// prepareForOCR 按默认目标高度准备候选图。
func prepareForOCR(img *Bitmap) (*Bitmap, bool) {
	return prepareAt(img, ocrTargetH)
}

// prepareAt 做「裁背景 -> 放大到 targetH -> 深色底反色 -> 补边」，
// 并报告这块区域里到底有没有笔迹（整块纯色的话连 OCR 都不用跑）。
func prepareAt(img *Bitmap, targetH int) (*Bitmap, bool) {
	bg := borderColor(img)

	// 先粗判一下有没有字：整块纯色就没必要浪费时间跑 OCR
	if _, _, _, _, n := inkBox(img, bg, inkAnyThr); n < inkMinPixels {
		return img, false
	}

	// 把多余的背景裁掉，这样放大倍数才是按文字的真实大小算的
	out := img
	if x0, y0, x1, y1, n := inkBox(img, bg, inkThreshold); n >= inkMinPixels {
		if float64((x1-x0)*(y1-y0)) <= inkMaxRatio*float64(img.Width*img.Height) {
			out = subImage(img, x0-inkPadPx, y0-inkPadPx, x1+inkPadPx, y1+inkPadPx)
		}
	}

	// 放大：让文字高度接近 targetH
	scale := 1
	for scale < ocrMaxScale && out.Height*scale < targetH {
		scale++
	}
	if scale > 1 {
		out = scaleBilinear(out, scale)
	}

	// 深色底（白字黑底）OCR 基本认不出来，统一转成浅色底
	if luminance(bg) < darkBgLum {
		out = invertImage(out)
	}

	// 补一圈边，用当前背景色
	pad := borderColor(out)
	return padImage(out, padPx, pad), true
}

// ocrVariants 按推荐顺序给出候选图，并报告这块区域里有没有笔迹：
//
//	放大 3 倍的（默认）-> 放大得少一点的 -> 放大得多的 -> 二值化 -> 反色
//
// 为什么要给几个不同的放大倍数：实测同样是 16px 的中文，放大 2 倍认得出来、
// 放大 4 倍反而认不出，而且哪种倍数好会随字号变化，多给几个候选最稳。
// 一次识别只要几毫秒，而且第一个认出来的够像样就停，所以不会拖慢多少。
func ocrVariants(img *Bitmap) ([]*Bitmap, bool) {
	base, hasInk := prepareAt(img, ocrTargetH)
	if !hasInk {
		return nil, false
	}
	out := []*Bitmap{base}
	for _, th := range []int{ocrTargetH - 8, ocrTargetH + 32} {
		if v, ok := prepareAt(img, th); ok {
			out = append(out, v)
		}
	}
	return append(out, otsuBinary(base), invertImage(base)), true
}

// luminance 粗略算颜色的亮度（0~255）。
func luminance(c uint32) int {
	r := int(c >> 16 & 0xff)
	g := int(c >> 8 & 0xff)
	b := int(c & 0xff)
	return (r*299 + g*587 + b*114) / 1000
}

func invertImage(img *Bitmap) *Bitmap {
	dst := &Bitmap{Pix: make([]byte, len(img.Pix)), Width: img.Width, Height: img.Height}
	for i := 0; i+3 < len(img.Pix); i += 4 {
		dst.Pix[i] = 255 - img.Pix[i]
		dst.Pix[i+1] = 255 - img.Pix[i+1]
		dst.Pix[i+2] = 255 - img.Pix[i+2]
		dst.Pix[i+3] = 255
	}
	return dst
}

// padImage 在四周补 n 圈 color 色的边。
func padImage(img *Bitmap, n int, color uint32) *Bitmap {
	w, h := img.Width+2*n, img.Height+2*n
	dst := &Bitmap{Pix: make([]byte, w*h*4), Width: w, Height: h}
	b := byte(color & 0xff)
	g := byte(color >> 8 & 0xff)
	r := byte(color >> 16 & 0xff)
	for i := 0; i+3 < len(dst.Pix); i += 4 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = b, g, r, 255
	}
	for y := 0; y < img.Height; y++ {
		src := y * img.Width * 4
		copy(dst.Pix[((y+n)*w+n)*4:((y+n)*w+n)*4+img.Width*4], img.Pix[src:src+img.Width*4])
	}
	return dst
}

// otsuBinary 灰度 + Otsu 阈值二值化，对付对比度低的字。
func otsuBinary(img *Bitmap) *Bitmap {
	gray := make([]int, img.Width*img.Height)
	var hist [256]int
	for i := range gray {
		v := luminance(packRGB(img.Pix, i*4))
		gray[i] = v
		hist[v]++
	}

	total := len(gray)
	var sumAll float64
	for v := 0; v < 256; v++ {
		sumAll += float64(v) * float64(hist[v])
	}
	var sumB, wB float64
	best, thr := -1.0, 128
	for v := 0; v < 256; v++ {
		wB += float64(hist[v])
		if wB == 0 {
			continue
		}
		wF := float64(total) - wB
		if wF == 0 {
			break
		}
		sumB += float64(v) * float64(hist[v])
		mB := sumB / wB
		mF := (sumAll - sumB) / wF
		between := wB * wF * (mB - mF) * (mB - mF)
		if between > best {
			best, thr = between, v
		}
	}

	dst := &Bitmap{Pix: make([]byte, len(img.Pix)), Width: img.Width, Height: img.Height}
	for i, v := range gray {
		var c byte = 255
		if v <= thr {
			c = 0
		}
		dst.Pix[i*4], dst.Pix[i*4+1], dst.Pix[i*4+2], dst.Pix[i*4+3] = c, c, c, 255
	}
	return dst
}

// scaleBilinear 按整数倍做双线性插值放大（只处理 BGR，alpha 统一给 255）。
func scaleBilinear(img *Bitmap, scale int) *Bitmap {
	w, h := img.Width, img.Height
	dw, dh := w*scale, h*scale
	dst := &Bitmap{Pix: make([]byte, dw*dh*4), Width: dw, Height: dh}

	at := func(x, y, c int) float64 {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return float64(img.Pix[(y*w+x)*4+c])
	}

	inv := 1.0 / float64(scale)
	for y := 0; y < dh; y++ {
		fy := (float64(y)+0.5)*inv - 0.5
		y0 := int(math.Floor(fy))
		wy := fy - float64(y0)
		for x := 0; x < dw; x++ {
			fx := (float64(x)+0.5)*inv - 0.5
			x0 := int(math.Floor(fx))
			wx := fx - float64(x0)
			di := (y*dw + x) * 4
			for c := 0; c < 3; c++ {
				v := at(x0, y0, c)*(1-wx)*(1-wy) +
					at(x0+1, y0, c)*wx*(1-wy) +
					at(x0, y0+1, c)*(1-wx)*wy +
					at(x0+1, y0+1, c)*wx*wy
				dst.Pix[di+c] = byte(v + 0.5)
			}
			dst.Pix[di+3] = 255
		}
	}
	return dst
}
