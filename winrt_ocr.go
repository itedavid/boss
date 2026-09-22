//go:build windows

// winrt_ocr.go 用 Go 标准库 syscall 调用 Windows 自带的 OCR（WinRT API）：
//
//	Windows.Media.Ocr.OcrEngine
//
// 不依赖 Python / Node / 任何第三方库，也不用额外安装 OCR 程序。
// WinRT 没有 C 头文件，这里按 ABI 直接调用 COM 虚表。
//
// 虚表下标约定：
//   - 前 3 个槽固定是 QueryInterface / AddRef / Release；
//   - WinRT 的 IInspectable 再占 3 个槽（GetIids / GetRuntimeClassName / GetTrustLevel）；
//   - 所以接口自己声明的第一个方法在下标 6，之后按声明顺序依次递增。
//
// 注意两点（都是踩过坑之后实测确认的）：
//  1. 下标要按 winmd 里的「方法声明顺序」数，不能按文档页面顺序猜。
//     例如 ISoftwareBitmapStatics 是 Copy、Convert、Convert 之后才是 CreateCopyFromBuffer（下标 9）。
//  2. WinRT 里接口之间的继承（如 IVectorView<T> : IIterable<T>）不会把父接口的
//     方法拼进虚表，每个接口的虚表都从下标 6 重新开始。
package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ---- COM / WinRT 基础 ----

var modCombase = syscall.NewLazyDLL("combase.dll")

var (
	procRoInitialize              = modCombase.NewProc("RoInitialize")
	procRoGetActivationFactory    = modCombase.NewProc("RoGetActivationFactory")
	procWindowsCreateString       = modCombase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = modCombase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = modCombase.NewProc("WindowsGetStringRawBuffer")
)

const (
	roInitMultithreaded = 1
	rpcEChangedMode     = 0x80010106 // 本线程已用别的套间初始化过，可以忽略
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func (g guid) String() string {
	return fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1], g.Data4[2], g.Data4[3],
		g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// mustGUID 把 "{XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}" 解析成 GUID。
func mustGUID(s string) guid {
	var g guid
	var d [8]byte
	_, err := fmt.Sscanf(strings.Trim(s, "{}"), "%8x-%4x-%4x-%2x%2x-%2x%2x%2x%2x%2x%2x",
		&g.Data1, &g.Data2, &g.Data3,
		&d[0], &d[1], &d[2], &d[3], &d[4], &d[5], &d[6], &d[7])
	if err != nil {
		panic("GUID 解析失败: " + s)
	}
	g.Data4 = d
	return g
}

var (
	iidOcrEngineStatics      = mustGUID("5BFFA85A-3384-3540-9940-699120D428A8")
	iidSoftwareBitmapStatics = mustGUID("DF0385DB-672F-4A9D-806E-C2442F343E86")
	iidBufferFactory         = mustGUID("71AF914D-C10F-484B-BC50-14BC623B3A27")
	iidBufferByteAccess      = mustGUID("905A0FEF-BC53-11DF-8C49-001E4FC686DA")
	iidAsyncInfo             = mustGUID("00000036-0000-0000-C000-000000000046")
)

// hresult 调用一个 Win32 API，把返回值当 HRESULT 看（>=0 表示成功）。
func hresult(p *syscall.LazyProc, args ...uintptr) int32 {
	r, _, _ := p.Call(args...)
	return int32(r)
}

// hrErr 把 HRESULT 转成 error（>=0 表示成功）。
func hrErr(what string, hr int32) error {
	if hr >= 0 {
		return nil
	}
	return fmt.Errorf("%s 失败（HRESULT 0x%08X）", what, uint32(hr))
}

// comCall 调用 COM 对象的第 index 个虚函数（0=QueryInterface, 1=AddRef, 2=Release）。
func comCall(obj unsafe.Pointer, index int, args ...uintptr) int32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*unsafe.Sizeof(uintptr(0))))
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(obj))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return int32(r)
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

func comQueryInterface(obj unsafe.Pointer, iid guid, out *unsafe.Pointer) error {
	return hrErr("QueryInterface", comCall(obj, 0,
		uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(out))))
}

// ---- HSTRING ----

func newHString(s string) (uintptr, error) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	hr := hresult(procWindowsCreateString,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	if err := hrErr("WindowsCreateString", hr); err != nil {
		return 0, err
	}
	return h, nil
}

func hstringText(h uintptr) string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuffer.Call(h, uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(uintptrToPointer(p)), n))
}

func deleteHString(h uintptr) {
	if h != 0 {
		procWindowsDeleteString.Call(h)
	}
}

// activationFactory 取 WinRT 运行时类的「静态方法工厂」。
func activationFactory(class string, iid guid) (unsafe.Pointer, error) {
	h, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer deleteHString(h)

	var obj unsafe.Pointer
	hr := hresult(procRoGetActivationFactory,
		h, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&obj)))
	if err := hrErr("RoGetActivationFactory("+class+")", hr); err != nil {
		return nil, err
	}
	return obj, nil
}

// ---- 虚表下标（按 winmd 的方法声明顺序，已逐个实测核对）----

const (
	// ISoftwareBitmapStatics：Copy(6) Convert(7) Convert(8) CreateCopyFromBuffer(9)
	vtSBCreateCopyFromBuffer = 9

	// IBufferFactory::Create(6)；IBuffer：get_Capacity(6) get_Length(7) put_Length(8)
	vtBufferFactoryCreate = 6
	vtBufferPutLength     = 8

	// IBufferByteAccess::Buffer(3)（这个接口直接继承 IUnknown，所以是下标 3）
	vtBufferByteAccessBuf = 3

	// IOcrEngineStatics：get_MaxImageDimension(6) get_AvailableRecognizerLanguages(7)
	// IsLanguageSupported(8) TryCreateFromLanguage(9) TryCreateFromUserProfileLanguages(10)
	vtOcrStaticsLanguages      = 7
	vtOcrStaticsTryFromLang    = 9
	vtOcrStaticsTryFromProfile = 10

	// IOcrEngine::RecognizeAsync(6)
	vtOcrRecognizeAsync = 6

	// IAsyncOperation<T>：put_Completed(6) get_Completed(7) GetResults(8)
	vtAsyncGetResults = 8
	// IAsyncInfo：get_Id(6) get_Status(7) get_ErrorCode(8) Cancel(9) Close(10)
	vtAsyncInfoGetStatus = 7

	// IVectorView<T>：GetAt(6) get_Size(7) IndexOf(8) GetMany(9)
	vtVectorGetAt = 6
	vtVectorSize  = 7

	// IOcrResult::get_Lines(6) get_Text(7)
	vtOcrResultLines = 6
	// IOcrLine::get_Words(6) get_Text(7)
	vtOcrLineText = 7
	// ILanguage::get_LanguageTag(6)
	vtLanguageTag = 6

	bitmapPixelFormatBgra8 = 87 // Windows.Graphics.Imaging.BitmapPixelFormat.Bgra8

	asyncStatusCompleted = 1
	asyncStatusCanceled  = 2
)

type winOCREngine struct {
	engine     unsafe.Pointer // IOcrEngine*
	sbStatics  unsafe.Pointer // ISoftwareBitmapStatics*
	bufFactory unsafe.Pointer // IBufferFactory*
	Language   string         // 实际使用的识别语言，例如 zh-Hans-CN
}

// 编译期确认它满足 OCR 接口
var _ OCR = (*winOCREngine)(nil)

// newWinOCREngine 必须在固定线程上调用（见 ocrLoop）。
func newWinOCREngine() (*winOCREngine, error) {
	hr := hresult(procRoInitialize, roInitMultithreaded)
	if hr < 0 && uint32(hr) != rpcEChangedMode {
		return nil, fmt.Errorf("RoInitialize 失败（HRESULT 0x%08X）", uint32(hr))
	}

	engineStatics, err := activationFactory("Windows.Media.Ocr.OcrEngine", iidOcrEngineStatics)
	if err != nil {
		return nil, err
	}
	defer comRelease(engineStatics)

	sbStatics, err := activationFactory("Windows.Graphics.Imaging.SoftwareBitmap", iidSoftwareBitmapStatics)
	if err != nil {
		return nil, err
	}
	bufFactory, err := activationFactory("Windows.Storage.Streams.Buffer", iidBufferFactory)
	if err != nil {
		comRelease(sbStatics)
		return nil, err
	}

	e := &winOCREngine{engine: nil, sbStatics: sbStatics, bufFactory: bufFactory}

	// 挑识别语言：优先中文，否则用系统给出的第一个
	var languages unsafe.Pointer
	if hr := comCall(engineStatics, vtOcrStaticsLanguages, uintptr(unsafe.Pointer(&languages))); hr < 0 {
		return nil, hrErr("OcrEngine.AvailableRecognizerLanguages", hr)
	}
	defer comRelease(languages)

	var count uint32
	if hr := comCall(languages, vtVectorSize, uintptr(unsafe.Pointer(&count))); hr < 0 {
		return nil, hrErr("IVectorView.Size", hr)
	}
	if count == 0 {
		return nil, errors.New("系统里没有可用的 OCR 识别语言")
	}

	pick := uint32(0)
	for i := uint32(0); i < count; i++ {
		lang, tag, err := languageAt(languages, i)
		comRelease(lang)
		if err != nil {
			continue
		}
		if strings.HasPrefix(tag, "zh") {
			pick = i
			break
		}
	}

	var engine unsafe.Pointer
	lang, tag, err := languageAt(languages, pick)
	if err != nil {
		return nil, err
	}
	hr = comCall(engineStatics, vtOcrStaticsTryFromLang, uintptr(lang), uintptr(unsafe.Pointer(&engine)))
	comRelease(lang)
	if hr < 0 || engine == nil {
		// 退一步：按当前用户的语言配置创建
		if hr2 := comCall(engineStatics, vtOcrStaticsTryFromProfile, uintptr(unsafe.Pointer(&engine))); hr2 < 0 || engine == nil {
			return nil, errors.New("创建 Windows OCR 引擎失败（系统里可能没有可用的中文 OCR 语言包）")
		}
		tag = "(用户语言)"
	}
	e.Language = tag
	e.engine = engine
	return e, nil
}

// languageAt 取识别语言列表里的第 i 项，并读回它的语言标签。
func languageAt(languages unsafe.Pointer, i uint32) (unsafe.Pointer, string, error) {
	var lang unsafe.Pointer
	if hr := comCall(languages, vtVectorGetAt, uintptr(i), uintptr(unsafe.Pointer(&lang))); hr < 0 {
		return nil, "", hrErr("Languages.GetAt", hr)
	}
	var hs uintptr
	if hr := comCall(lang, vtLanguageTag, uintptr(unsafe.Pointer(&hs))); hr < 0 {
		comRelease(lang)
		return nil, "", hrErr("Language.LanguageTag", hr)
	}
	tag := hstringText(hs)
	deleteHString(hs)
	return lang, tag, nil
}

// Recognize 识别一张位图里的文字，多行用换行拼起来。
//
// 先做预处理，再按「原图 -> 二值化 -> 反色」的顺序试：
// 一旦某张候选图认出来的字已经够像样就直接返回，所以常见情况只会跑一遍 OCR。
func (e *winOCREngine) Recognize(img *Bitmap) (string, error) {
	if img == nil || img.Width <= 0 || img.Height <= 0 || len(img.Pix) < img.Width*img.Height*4 {
		return "", errors.New("位图数据不完整")
	}

	variants, hasInk := ocrVariants(img)
	if !hasInk {
		// 整块是纯色，肯定没有字，连 OCR 都不用跑
		return "", nil
	}

	var (
		best      string
		bestScore = -1
		lastErr   error
	)
	for _, v := range variants {
		text, err := e.recognizeOne(v)
		if err != nil {
			lastErr = err
			continue
		}
		if s := scoreText(text); s > bestScore {
			best, bestScore = text, s
		}
		if bestScore >= scoreGood {
			break
		}
	}
	if bestScore < 0 {
		if lastErr == nil {
			lastErr = errors.New("OCR 没有返回结果")
		}
		return "", lastErr
	}
	return best, nil
}

// recognizeOne 对一张已经预处理好的图跑一次 OCR。
func (e *winOCREngine) recognizeOne(img *Bitmap) (string, error) {
	// 1) BGRA 像素 -> IBuffer
	buf, err := e.bufferFromImage(img)
	if err != nil {
		return "", err
	}
	defer comRelease(buf)

	// 2) IBuffer -> SoftwareBitmap
	bitmap, err := e.bitmapFromBuffer(buf, img.Width, img.Height)
	if err != nil {
		return "", err
	}
	defer comRelease(bitmap)

	// 3) 识别（异步，轮询等它完成）
	var op unsafe.Pointer
	if hr := comCall(e.engine, vtOcrRecognizeAsync,
		uintptr(bitmap), uintptr(unsafe.Pointer(&op))); hr < 0 || op == nil {
		return "", hrErr("OcrEngine.RecognizeAsync", hr)
	}
	defer comRelease(op)

	// get_Status 属于 IAsyncInfo，按 WinRT 的规矩要单独 QueryInterface 过去取
	var info unsafe.Pointer
	if err := comQueryInterface(op, iidAsyncInfo, &info); err != nil {
		return "", err
	}
	defer comRelease(info)

	deadline := time.Now().Add(15 * time.Second)
	for {
		var status int32
		if hr := comCall(info, vtAsyncInfoGetStatus, uintptr(unsafe.Pointer(&status))); hr < 0 {
			return "", hrErr("IAsyncInfo.get_Status", hr)
		}
		if status == asyncStatusCompleted {
			break
		}
		if status >= asyncStatusCanceled {
			return "", fmt.Errorf("OCR 操作异常结束（状态 %d）", status)
		}
		if time.Now().After(deadline) {
			return "", errors.New("OCR 超时")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var result unsafe.Pointer
	if hr := comCall(op, vtAsyncGetResults, uintptr(unsafe.Pointer(&result))); hr < 0 || result == nil {
		return "", hrErr("IAsyncOperation.GetResults", hr)
	}
	defer comRelease(result)

	return ocrResultText(result)
}

// ocrResultText 把 OcrResult 的每一行拼成多行文本。
func ocrResultText(result unsafe.Pointer) (string, error) {
	var lines unsafe.Pointer
	if hr := comCall(result, vtOcrResultLines, uintptr(unsafe.Pointer(&lines))); hr < 0 || lines == nil {
		return "", hrErr("OcrResult.Lines", hr)
	}
	defer comRelease(lines)

	var n uint32
	if hr := comCall(lines, vtVectorSize, uintptr(unsafe.Pointer(&n))); hr < 0 {
		return "", hrErr("OcrResult.Lines.Size", hr)
	}

	var b strings.Builder
	for i := uint32(0); i < n; i++ {
		var line unsafe.Pointer
		if hr := comCall(lines, vtVectorGetAt, uintptr(i), uintptr(unsafe.Pointer(&line))); hr < 0 || line == nil {
			continue
		}
		var hs uintptr
		if hr := comCall(line, vtOcrLineText, uintptr(unsafe.Pointer(&hs))); hr >= 0 && hs != 0 {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(hstringText(hs))
			deleteHString(hs)
		}
		comRelease(line)
	}
	return b.String(), nil
}

// bufferFromImage 把一张 BGRA 位图装进 WinRT 的 IBuffer。
func (e *winOCREngine) bufferFromImage(img *Bitmap) (unsafe.Pointer, error) {
	return newWinBuffer(e.bufFactory, img.Pix)
}

// newWinBuffer 造一个 IBuffer 并把 data 拷进去（同时把 Length 设对）。
func newWinBuffer(bufFactory unsafe.Pointer, data []byte) (unsafe.Pointer, error) {
	var buf unsafe.Pointer
	if hr := comCall(bufFactory, vtBufferFactoryCreate,
		uintptr(len(data)), uintptr(unsafe.Pointer(&buf))); hr < 0 || buf == nil {
		return nil, hrErr("Buffer.Create", hr)
	}

	var byteAccess unsafe.Pointer
	if err := comQueryInterface(buf, iidBufferByteAccess, &byteAccess); err != nil {
		comRelease(buf)
		return nil, err
	}
	defer comRelease(byteAccess)

	var raw unsafe.Pointer
	if hr := comCall(byteAccess, vtBufferByteAccessBuf, uintptr(unsafe.Pointer(&raw))); hr < 0 || raw == nil {
		comRelease(buf)
		return nil, hrErr("IBufferByteAccess.Buffer", hr)
	}
	copy(unsafe.Slice((*byte)(raw), len(data)), data)

	if hr := comCall(buf, vtBufferPutLength, uintptr(len(data))); hr < 0 {
		comRelease(buf)
		return nil, hrErr("Buffer.put_Length", hr)
	}
	return buf, nil
}

// bitmapFromBuffer 用 ISoftwareBitmapStatics::CreateCopyFromBuffer 造一张 SoftwareBitmap。
func (e *winOCREngine) bitmapFromBuffer(buf unsafe.Pointer, width, height int) (unsafe.Pointer, error) {
	return newSoftwareBitmapFromBuffer(e.sbStatics, buf, width, height)
}

func newSoftwareBitmapFromBuffer(sbStatics, buf unsafe.Pointer, width, height int) (unsafe.Pointer, error) {
	var bitmap unsafe.Pointer
	hr := comCall(sbStatics, vtSBCreateCopyFromBuffer,
		uintptr(buf), bitmapPixelFormatBgra8,
		uintptr(width), uintptr(height),
		uintptr(unsafe.Pointer(&bitmap)))
	if hr < 0 || bitmap == nil {
		return nil, hrErr("SoftwareBitmap.CreateCopyFromBuffer", hr)
	}
	return bitmap, nil
}
