//go:build windows

package backend

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

// Windows 常量（badge 图标专用）
const (
	WM_SETICON             = 0x0080
	WM_GETICON             = 0x007F
	ICON_BIG               = 1
	ICON_SMALL             = 0
	IMAGE_ICON             = 1
	LR_LOADFROMFILE        = 0x0010
	GCL_HICON              = uintptr(0xFFFFFFF2) // -14
	diNormal               = 3
	DIB_RGB_COLORS         = 0
	BI_RGB                 = 0
	badgeIconDesignVersion = 8
)

// badgeIconFileCache 缓存已生成的 badge 图标文件路径（key 为显示序号，value 为 .ico 文件路径）
var badgeIconFileCache struct {
	sync.RWMutex
	data map[int]string // 序号 -> .ico 文件路径
}

func init() {
	badgeIconFileCache.data = make(map[int]string)
}

// ============================================================================
// GDI 图标提取
// ============================================================================

// getWindowIcon 获取窗口的 HICON，依次尝试 ICON_BIG、ICON_SMALL、GetClassLongPtr(GCL_HICON)
func getWindowIcon(hwnd windows.HWND) (uintptr, error) {
	// 尝试 WM_GETICON ICON_BIG
	ret, _, _ := procSendMessageW.Call(uintptr(hwnd), uintptr(WM_GETICON), uintptr(ICON_BIG), 0)
	if ret != 0 {
		return ret, nil
	}

	// 尝试 WM_GETICON ICON_SMALL
	ret, _, _ = procSendMessageW.Call(uintptr(hwnd), uintptr(WM_GETICON), uintptr(ICON_SMALL), 0)
	if ret != 0 {
		return ret, nil
	}

	// 尝试 GetClassLongPtr(GCL_HICON) 作为兜底
	ret, _, _ = procGetClassLongPtrW.Call(uintptr(hwnd), GCL_HICON)
	if ret != 0 {
		return ret, nil
	}

	return 0, fmt.Errorf("窗口无图标句柄")
}

// hiconToImage 将 HICON 转换为 Go image.NRGBA（64x64）
// 使用 GDI: CreateDIBSection + DrawIconEx + 读取像素数据，处理 BGRA 预乘alpha
func hiconToImage(hIcon uintptr) (*image.NRGBA, error) {
	const size = 64

	// 获取屏幕 DC
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC 失败")
	}
	defer procReleaseDC.Call(0, screenDC)

	// 创建兼容内存 DC
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC 失败")
	}
	defer procDeleteDC.Call(memDC)

	// 构造 BITMAPINFOHEADER（自顶向下，32位 BGRA）
	type bitmapInfoHeader struct {
		BiSize          uint32
		BiWidth         int32
		BiHeight        int32
		BiPlanes        uint16
		BiBitCount      uint16
		BiCompression   uint32
		BiSizeImage     uint32
		BiXPelsPerMeter int32
		BiYPelsPerMeter int32
		BiClrUsed       uint32
		BiClrImportant  uint32
	}

	bmi := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       size,
		BiHeight:      -size, // 负数表示自顶向下
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: BI_RGB,
	}

	// 创建 DIB Section
	var ppBits unsafe.Pointer
	hBitmap, _, _ := procCreateDIBSection.Call(
		memDC,
		uintptr(unsafe.Pointer(&bmi)),
		uintptr(DIB_RGB_COLORS),
		uintptr(unsafe.Pointer(&ppBits)),
		0, 0,
	)
	if hBitmap == 0 || ppBits == nil {
		return nil, fmt.Errorf("CreateDIBSection 失败")
	}
	defer procDeleteObject.Call(hBitmap)

	// 选入内存 DC
	oldBmp, _, _ := procSelectObject.Call(memDC, hBitmap)
	defer procSelectObject.Call(memDC, oldBmp)

	// 用 DrawIconEx 绘制图标到 DIB
	ret, _, _ := procDrawIconEx.Call(
		memDC,             // hdc
		0,                 // xLeft
		0,                 // yTop
		hIcon,             // hIcon
		uintptr(size),     // cxWidth
		uintptr(size),     // cyHeight
		0,                 // istepIfAni
		0,                 // hbrFlickerFreeDraw (NULL)
		uintptr(diNormal), // diFlags
	)
	if ret == 0 {
		return nil, fmt.Errorf("DrawIconEx 失败")
	}

	// 从 ppBits 读取像素数据（BGRA 预乘 alpha → NRGBA straight alpha）
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	pixelCount := size * size
	bgraData := unsafe.Slice((*byte)(ppBits), pixelCount*4)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			off := (y*size + x) * 4
			b := bgraData[off+0]
			g := bgraData[off+1]
			r := bgraData[off+2]
			a := bgraData[off+3]

			if a == 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
				continue
			}

			// 反预乘 alpha: R = min(R_premul * 255 / A, 255)
			rOut := uint8(min(uint16(r)*255/uint16(a), 255))
			gOut := uint8(min(uint16(g)*255/uint16(a), 255))
			bOut := uint8(min(uint16(b)*255/uint16(a), 255))

			img.SetNRGBA(x, y, color.NRGBA{R: rOut, G: gOut, B: bOut, A: a})
		}
	}

	return img, nil
}

// ============================================================================
// 图标绘制辅助
// ============================================================================

// drawCircle 在 img 上绘制填充圆（带抗锯齿）
func drawCircle(img *image.NRGBA, cx, cy, r int, col color.NRGBA) {
	for y := cy - r - 1; y <= cy+r+1; y++ {
		for x := cx - r - 1; x <= cx+r+1; x++ {
			if x < 0 || x >= img.Bounds().Dx() || y < 0 || y >= img.Bounds().Dy() {
				continue
			}
			dx := float64(x) - float64(cx)
			dy := float64(y) - float64(cy)
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist <= float64(r) {
				img.Set(x, y, col)
			} else if dist <= float64(r)+0.8 {
				// 抗锯齿
				alpha := 1.0 - (dist-float64(r))/0.8
				bg := img.NRGBAAt(x, y)
				ratio := alpha
				outR := uint8(float64(col.R)*ratio + float64(bg.R)*(1-ratio))
				outG := uint8(float64(col.G)*ratio + float64(bg.G)*(1-ratio))
				outB := uint8(float64(col.B)*ratio + float64(bg.B)*(1-ratio))
				outA := uint8(float64(col.A)*ratio + float64(bg.A)*(1-ratio))
				img.Set(x, y, color.NRGBA{R: outR, G: outG, B: outB, A: outA})
			}
		}
	}
}

// generateFallbackIcon generates the stable monochrome-blue BrowserStudio
// taskbar base. It deliberately does not reuse chrome.exe's multicolour icon.
func generateFallbackIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	outerR := float64(size) * 0.46
	blue := color.NRGBA{R: 37, G: 99, B: 235, A: 255}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > outerR+1 {
				continue
			}
			alpha := uint8(255)
			if dist > outerR {
				alpha = uint8(max(0, int((1-(dist-outerR))*255)))
			}
			img.SetNRGBA(x, y, color.NRGBA{R: blue.R, G: blue.G, B: blue.B, A: alpha})
		}
	}
	return img
}

var badgeDigitPixels = map[byte][5]string{
	'0': {"111", "101", "101", "101", "111"},
	'1': {"010", "110", "010", "010", "111"},
	'2': {"111", "001", "111", "100", "111"},
	'3': {"111", "001", "111", "001", "111"},
	'4': {"101", "101", "111", "001", "001"},
	'5': {"111", "100", "111", "001", "111"},
	'6': {"111", "100", "111", "101", "111"},
	'7': {"111", "001", "010", "010", "010"},
	'8': {"111", "101", "111", "101", "111"},
	'9': {"111", "101", "111", "001", "111"},
}

// overlayBadgeNumberNative 直接在 Windows 会实际选择的图标尺寸上绘制数字。
// 像素字不经过二次缩小，16/24/32/48/64px 下都能保持清晰边缘。
func overlayBadgeNumberNative(img *image.NRGBA, number int) {
	size := img.Bounds().Dx()
	numStr := fmt.Sprintf("%d", number)
	if len(numStr) > 4 {
		numStr = numStr[len(numStr)-4:]
	}

	layout := calculateBadgeNumberLayout(size, len(numStr))
	scale := layout.scale
	gap := layout.gap
	fontW := layout.fontW
	fontH := layout.fontH
	x := (size - fontW) / 2
	y := (size - fontH) / 2
	for _, ch := range []byte(numStr) {
		rows, ok := badgeDigitPixels[ch]
		if !ok {
			continue
		}
		for row, pixels := range rows {
			for col, pixel := range []byte(pixels) {
				if pixel != '1' {
					continue
				}
				for yy := 0; yy < scale; yy++ {
					for xx := 0; xx < scale; xx++ {
						img.SetNRGBA(x+col*scale+xx, y+row*scale+yy, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
					}
				}
			}
		}
		x += 3*scale + gap
	}
}

func drawRoundedPill(img *image.NRGBA, x, y, w, h, r int, col color.NRGBA) {
	if w <= 0 || h <= 0 {
		return
	}
	if r > h/2 {
		r = h / 2
	}
	for yy := y; yy < y+h; yy++ {
		for xx := x + r; xx < x+w-r; xx++ {
			if xx >= 0 && xx < img.Bounds().Dx() && yy >= 0 && yy < img.Bounds().Dy() {
				img.Set(xx, yy, col)
			}
		}
	}
	drawCircle(img, x+r, y+r, r, col)
	drawCircle(img, x+w-r-1, y+r, r, col)
}

// generateBadgeIconImage 生成固定的 Boost Browser 蓝色浏览器图标并叠加红色编号角标。
// 不再使用 Chrome 窗口自身图标作为底图：切到系统最新版 Chrome 后，Chrome 会暴露
// 多色官方图标；用户要求任务栏仍保持之前的蓝色 Chrome 类图标 + 红色编号 badge 样式。
func generateBadgeIconImage(pid int, number int, size int) *image.NRGBA {
	baseImg := generateFallbackIcon(size)
	overlayBadgeNumberNative(baseImg, number)
	return baseImg
}

// ============================================================================
// ICO 文件生成
// ============================================================================

// generateBadgeICO 生成多分辨率编号图标。每个分辨率独立原生绘制，避免
// 192px 源图缩到任务栏尺寸后数字发糊，也避免 Windows 高 DPI 再次插值。
func generateBadgeICO(pid int, number int) ([]byte, error) {
	sizes := []int{16, 24, 32, 48, 64, 128}
	pngData := make([][]byte, 0, len(sizes))
	for _, size := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, generateBadgeIconImage(pid, number, size)); err != nil {
			return nil, err
		}
		pngData = append(pngData, buf.Bytes())
	}

	// ICO 文件格式
	// 参考：https://en.wikipedia.org/wiki/ICO_(file_format)
	type icoDirEntry struct {
		Width       uint8
		Height      uint8
		ColorCount  uint8
		Reserved    uint8
		Planes      uint16
		BitCount    uint16
		BytesInRes  uint32
		ImageOffset uint32
	}

	type icoDirHeader struct {
		Reserved  uint16
		ImageType uint16
		NumImages uint16
	}

	header := icoDirHeader{
		Reserved:  0,
		ImageType: 1, // ICON
		NumImages: uint16(len(sizes)),
	}

	var result bytes.Buffer
	_ = binary.Write(&result, binary.LittleEndian, header)
	offset := uint32(binary.Size(header) + binary.Size(icoDirEntry{})*len(sizes))
	for i, size := range sizes {
		entry := icoDirEntry{Width: uint8(size), Height: uint8(size), Planes: 1, BitCount: 32, BytesInRes: uint32(len(pngData[i])), ImageOffset: offset}
		_ = binary.Write(&result, binary.LittleEndian, entry)
		offset += uint32(len(pngData[i]))
	}
	for _, data := range pngData {
		result.Write(data)
	}

	return result.Bytes(), nil
}

// getBadgeICOFilePath 获取带编号的 ICO 文件路径（带缓存，key 包含 pid）。
// 文件保存在临时目录下的 badge_icons 子目录中。
func getBadgeICOFilePath(pid int, number int) (string, error) {
	cacheKey := number // 缓存 key 仍按编号（同一编号图标相同即可复用）

	badgeIconFileCache.RLock()
	if path, ok := badgeIconFileCache.data[cacheKey]; ok {
		badgeIconFileCache.RUnlock()
		// 检查文件是否仍然存在
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		// 文件不存在，需要重新生成
	} else {
		badgeIconFileCache.RUnlock()
	}

	icoData, err := generateBadgeICO(pid, number)
	if err != nil {
		return "", err
	}

	// 保存到临时目录
	tmpDir := filepath.Join(os.TempDir(), "browserstudio_badge_icons")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", err
	}
	icoPath := filepath.Join(tmpDir, fmt.Sprintf("badge_v%d_%d.ico", badgeIconDesignVersion, number))
	if err := os.WriteFile(icoPath, icoData, 0644); err != nil {
		return "", err
	}

	badgeIconFileCache.Lock()
	badgeIconFileCache.data[cacheKey] = icoPath
	badgeIconFileCache.Unlock()

	return icoPath, nil
}

// ============================================================================
// 图标设置（Windows API）
// ============================================================================

// loadIconFromFile 从文件加载图标句柄
func loadIconFromFile(icoPath string, size int) (windows.HWND, error) {
	pathPtr, _ := windows.UTF16PtrFromString(icoPath)

	handle, _, err := procLoadImageW.Call(
		0,                                // hInstance (NULL)
		uintptr(unsafe.Pointer(pathPtr)), // lpszName
		uintptr(IMAGE_ICON),              // uType
		uintptr(size),                    // cxDesired
		uintptr(size),                    // cyDesired
		uintptr(LR_LOADFROMFILE),         // fuLoad
	)
	if handle == 0 {
		return 0, fmt.Errorf("LoadImage 失败: %v (path=%s)", err, icoPath)
	}
	return windows.HWND(handle), nil
}

// setWindowIcon 为指定进程的窗口设置自定义图标。
func setWindowIcon(pid int, icoPath string) error {
	hwnd, err := findProcessTreeWindow(pid)
	if err != nil {
		return fmt.Errorf("查找窗口失败: %v", err)
	}

	// 任务栏在部分 DPI/缩放设置下会优先使用 ICON_SMALL。
	// 旧逻辑给 ICON_SMALL 加载 16x16，右上角数字缩放后容易只剩红色底、不见白字。
	// 这里小/大图标都加载 32x32，让 Explorer 需要小图标时自己缩放，数字保真度更高。
	smallIcon, err := loadIconFromFile(icoPath, 32)
	if err != nil {
		return fmt.Errorf("加载小图标失败: %v", err)
	}

	// Load a true 64px large icon for high-DPI taskbars and Alt+Tab previews.
	bigIcon, err := loadIconFromFile(icoPath, 64)
	if err != nil {
		return fmt.Errorf("加载大图标失败: %v", err)
	}

	// 设置图标
	procSendMessageW.Call(uintptr(hwnd), uintptr(WM_SETICON), uintptr(ICON_SMALL), uintptr(smallIcon))
	procSendMessageW.Call(uintptr(hwnd), uintptr(WM_SETICON), uintptr(ICON_BIG), uintptr(bigIcon))

	return nil
}

// setBadgeForInstance 为指定进程设置带编号的任务栏图标。
// pid 为 Chromium/Chrome 主进程的进程 ID，displayNumber 为显示序号。
// 通过 Windows GDI 从窗口提取原始浏览器图标，叠加红色编号角标后设置到任务栏。
//
// 注意：这里故意只做“启动阶段有限重试”，不再保留长期 badge watchdog。
// 线上 crashprobe 已证明主程序闪退的首个稳定止血点是关闭常驻托盘；而旧的
// badge watchdog 还会为每个实例常驻一个 Win32/GDI 刷新循环，风险会随着实例数
// 量叠加。用户当前明确要求把实例数字加回来，因此这里先恢复一次性 badge 设置：
// 在窗口刚出现时重试若干次，成功后立即退出，避免后台无限循环再次放大宿主风
// 险。后续若确认需要“数字长期自愈”，再单独做隔离实现。
func setBadgeForInstance(pid int, displayNumber int) error {
	log := logger.New("BadgeIcon")
	icoPath, err := getBadgeICOFilePath(pid, displayNumber)
	if err != nil {
		log.Warn("badge ICO 文件生成失败", logger.F("error", err.Error()))
		return err
	}

	var lastErr error
	successfulWrites := 0
	for attempt := 0; attempt < 60; attempt++ {
		if !isProcessAlive(pid) {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("浏览器进程已退出: pid=%d", pid)
		}

		err = setWindowIcon(pid, icoPath)
		if err == nil {
			successfulWrites++
			// Chrome 初始化首个标签页时可能覆盖一次 WM_SETICON。两次短间隔写入
			// 即可覆盖该窗口期，同时让编号比旧版最多提前数秒出现。
			if successfulWrites >= 2 {
				log.Info("任务栏 badge 图标设置成功",
					logger.F("pid", pid),
					logger.F("display_number", displayNumber),
					logger.F("attempt", attempt+1),
					logger.F("writes", successfulWrites),
				)
				return nil
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}

		lastErr = err
		if attempt < 59 {
			if attempt < 20 {
				time.Sleep(100 * time.Millisecond)
			} else {
				time.Sleep(250 * time.Millisecond)
			}
		}
	}

	log.Warn("任务栏 badge 图标启动阶段设置失败（已放弃重试）",
		logger.F("pid", pid),
		logger.F("display_number", displayNumber),
		logger.F("error", lastErr.Error()),
	)
	return lastErr
}
