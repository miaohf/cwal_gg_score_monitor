//go:build windows

package main

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2/app"
)

const (
	wmDestroy   = 0x0002
	wmPaint     = 0x000F
	wmSize      = 0x0005
	wmMove      = 0x0003
	wmNCHitTest = 0x0084
	wmKeyDown   = 0x0100
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	htCaption   = 2

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsExLayered        = 0x00080000
	wsExTopmost        = 0x00000008
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000

	sWShow = 5

	stockDefaultGUIFont = 17
	transparentBkMode   = 1
	idcArrow            = 32512

	vkUp       = 0x26
	vkDown     = 0x28
	vkOemPlus  = 0xBB
	vkOemMinus = 0xBD
	vkAdd      = 0x6B
	vkSubtract = 0x6D
	vkF2       = 0x71

	biRGB        = 0
	dibRGBColors = 0
	ulwAlpha     = 0x00000002
	acSrcOver    = 0
	acSrcAlpha   = 1

	// Keep a small alpha floor: fully transparent pixels in a layered window can
	// become effectively unclickable, making the overlay hard to focus or move.
	minOverlayBackgroundAlpha = 48
	maxOverlayBackgroundAlpha = 255
	overlayOpacityStep        = 15

	defaultOverlayX      = 60
	defaultOverlayY      = 60
	defaultOverlayWidth  = 420
	defaultOverlayHeight = 700

	prefWindowsOverlayXKey      = "windows_overlay.x"
	prefWindowsOverlayYKey      = "windows_overlay.y"
	prefWindowsOverlayWidthKey  = "windows_overlay.width"
	prefWindowsOverlayHeightKey = "windows_overlay.height"
)

type winPoint struct {
	X int32
	Y int32
}

type winSize struct {
	CX int32
	CY int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	RcPaint     winRect
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type overlayRow struct {
	rank    string
	name    string
	rating  string
	isError bool
}

type windowsOverlayState struct {
	rows   []*playerState
	rowsMu sync.RWMutex
	cfg    apiConfig
	prefs  fynePreferences
	hwnd   uintptr
	stopCh chan struct{}

	sizeMu sync.RWMutex
	x      int
	y      int
	width  int
	height int

	alphaMu sync.RWMutex
	bgAlpha uint8

	displayMu   sync.RWMutex
	displayRows []overlayRow
	lastUpdated string
	anySuccess  bool

	openSettings bool
}

type fynePreferences interface {
	Bool(string) bool
	Int(string) int
	SetBool(string, bool)
	SetInt(string, int)
}

type overlayPlacement struct {
	x      int
	y      int
	width  int
	height int
}

var (
	errOpenFyneSettings = errors.New("open fyne settings")

	user32                    = syscall.NewLazyDLL("user32.dll")
	gdi32                     = syscall.NewLazyDLL("gdi32.dll")
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	msimg32                   = syscall.NewLazyDLL("msimg32.dll")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procShowWindow            = user32.NewProc("ShowWindow")
	procUpdateWindow          = user32.NewProc("UpdateWindow")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procBeginPaint            = user32.NewProc("BeginPaint")
	procEndPaint              = user32.NewProc("EndPaint")
	procInvalidateRect        = user32.NewProc("InvalidateRect")
	procLoadCursorW           = user32.NewProc("LoadCursorW")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procUpdateLayeredWindow   = user32.NewProc("UpdateLayeredWindow")
	procCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC              = gdi32.NewProc("DeleteDC")
	procCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procGetStockObject        = gdi32.NewProc("GetStockObject")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procTextOutW              = gdi32.NewProc("TextOutW")
	procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
	procGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	_                         = msimg32

	globalWindowsOverlayWindow *windowsOverlayState
)

func runWindowsTransparentMode(rows []*playerState, cfg apiConfig) bool {
	if err := initAPILogger(apiLogPath); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init api logger: %v\n", err)
	}
	prefs := app.NewWithID("cwalgg.score.monitor").Preferences()
	initialAlpha := uint8(defaultWindowOpacity)
	if prefs.Bool(prefSettingsSavedKey) {
		initialAlpha = clampByte(prefs.Int(prefWindowOpacityKey), defaultWindowOpacity)
	}
	if initialAlpha < minOverlayBackgroundAlpha {
		initialAlpha = minOverlayBackgroundAlpha
	}
	placement := loadOverlayPlacement(prefs)
	state := &windowsOverlayState{
		rows:    rows,
		cfg:     cfg,
		prefs:   prefs,
		stopCh:  make(chan struct{}),
		x:       placement.x,
		y:       placement.y,
		width:   placement.width,
		height:  placement.height,
		bgAlpha: initialAlpha,
	}
	if err := state.run(); err != nil {
		if errors.Is(err, errOpenFyneSettings) {
			return false
		}
		fmt.Fprintf(os.Stderr, "windows transparent mode failed, fallback to fyne: %v\n", err)
		return false
	}
	return true
}

func (s *windowsOverlayState) run() error {
	if len(s.rows) == 0 {
		return errors.New("no players configured")
	}
	globalWindowsOverlayWindow = s

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("CWALGGTransparentWindow")
	title, _ := syscall.UTF16PtrFromString("Score Monitor")
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		Style:     csHRedraw | csVRedraw,
		WndProc:   syscall.NewCallback(windowsOverlayWndProc),
		Instance:  hInstance,
		Cursor:    cursor,
		ClassName: className,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW failed: %w", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		uintptr(wsExLayered|wsExTopmost),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsOverlappedWindow|wsVisible),
		uintptr(s.x), uintptr(s.y), uintptr(s.width), uintptr(s.height),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW failed: %w", err)
	}
	s.hwnd = hwnd

	go s.pollLoop()
	s.render()
	_, _, _ = procShowWindow.Call(hwnd, sWShow)
	_, _, _ = procUpdateWindow.Call(hwnd)

	var msg winMsg
	for {
		ret, _, loopErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) == -1 {
			return fmt.Errorf("GetMessageW failed: %w", loopErr)
		}
		if ret == 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	close(s.stopCh)
	if s.openSettings && s.hwnd != 0 {
		_, _, _ = procDestroyWindow.Call(s.hwnd)
		s.hwnd = 0
	}
	if s.openSettings {
		return errOpenFyneSettings
	}
	return nil
}

func (s *windowsOverlayState) backgroundTransparencyPercent() int {
	s.alphaMu.RLock()
	alpha := s.bgAlpha
	s.alphaMu.RUnlock()
	return int(transparencyPercentFromAlpha(alpha))
}

func loadOverlayPlacement(prefs fynePreferences) overlayPlacement {
	p := overlayPlacement{
		x:      defaultOverlayX,
		y:      defaultOverlayY,
		width:  defaultOverlayWidth,
		height: defaultOverlayHeight,
	}
	if prefs == nil {
		return p
	}
	if v := prefs.Int(prefWindowsOverlayXKey); v >= -32000 && v <= 32000 {
		p.x = v
	}
	if v := prefs.Int(prefWindowsOverlayYKey); v >= -32000 && v <= 32000 {
		p.y = v
	}
	if v := prefs.Int(prefWindowsOverlayWidthKey); v >= 240 && v <= 4000 {
		p.width = v
	}
	if v := prefs.Int(prefWindowsOverlayHeightKey); v >= 220 && v <= 4000 {
		p.height = v
	}
	return p
}

func (s *windowsOverlayState) saveWindowPlacement() {
	if s.prefs == nil || s.hwnd == 0 {
		return
	}
	var rect winRect
	ret, _, _ := procGetWindowRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return
	}
	width := int(rect.Right - rect.Left)
	height := int(rect.Bottom - rect.Top)
	if width < 240 || height < 220 {
		return
	}
	s.prefs.SetInt(prefWindowsOverlayXKey, int(rect.Left))
	s.prefs.SetInt(prefWindowsOverlayYKey, int(rect.Top))
	s.prefs.SetInt(prefWindowsOverlayWidthKey, width)
	s.prefs.SetInt(prefWindowsOverlayHeightKey, height)
}

func (s *windowsOverlayState) adjustBackgroundAlpha(delta int) {
	s.alphaMu.Lock()
	next := int(s.bgAlpha) + delta
	if next < minOverlayBackgroundAlpha {
		next = minOverlayBackgroundAlpha
	}
	if next > maxOverlayBackgroundAlpha {
		next = maxOverlayBackgroundAlpha
	}
	s.bgAlpha = uint8(next)
	nextAlpha := s.bgAlpha
	s.alphaMu.Unlock()

	if s.prefs != nil {
		s.prefs.SetBool(prefSettingsSavedKey, true)
		s.prefs.SetInt(prefWindowOpacityKey, int(nextAlpha))
	}
	s.render()
}

func (s *windowsOverlayState) pollLoop() {
	runCycle := func() {
		refreshRows(s.rows, &s.rowsMu, s.cfg)
		s.rebuildDisplayRows()
		s.render()
	}
	runCycle()
	ticker := time.NewTicker(fetchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			runCycle()
		}
	}
}

func (s *windowsOverlayState) rebuildDisplayRows() {
	s.rowsMu.RLock()
	indices := make([]int, len(s.rows))
	for i := range s.rows {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		ra, rb := s.rows[indices[a]], s.rows[indices[b]]
		aErr := ra.LastError != ""
		bErr := rb.LastError != ""
		if aErr != bErr {
			return !aErr
		}
		if ra.LiveScore != rb.LiveScore {
			return ra.LiveScore > rb.LiveScore
		}
		return ra.Name < rb.Name
	})

	next := make([]overlayRow, 0, len(indices))
	anyOK := false
	for pos, idx := range indices {
		r := s.rows[idx]
		item := overlayRow{
			rank:    strconv.Itoa(pos + 1),
			name:    r.Name,
			rating:  strconv.Itoa(r.LiveScore),
			isError: r.LastError != "",
		}
		if item.isError {
			item.rank = "-"
			item.rating = "-"
		} else {
			anyOK = true
		}
		next = append(next, item)
	}
	s.rowsMu.RUnlock()

	s.displayMu.Lock()
	s.displayRows = next
	s.anySuccess = anyOK
	if anyOK {
		s.lastUpdated = time.Now().Format("2006-01-02 15:04:05")
	}
	s.displayMu.Unlock()
}

func windowsOverlayWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := globalWindowsOverlayWindow
	switch msg {
	case wmNCHitTest:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmSize:
		if state != nil {
			w := int(lParam & 0xffff)
			h := int((lParam >> 16) & 0xffff)
			if w > 0 && h > 0 {
				state.sizeMu.Lock()
				state.width = w
				state.height = h
				state.sizeMu.Unlock()
				state.render()
			}
			state.saveWindowPlacement()
		}
		return 0
	case wmMove:
		if state != nil {
			state.saveWindowPlacement()
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmLButtonUp:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmRButtonUp:
		if state != nil {
			state.openSettings = true
			_, _, _ = procPostQuitMessage.Call(0)
			return 0
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmKeyDown:
		if state != nil {
			switch wParam {
			case vkUp, vkOemPlus, vkAdd:
				state.adjustBackgroundAlpha(overlayOpacityStep)
				return 0
			case vkDown, vkOemMinus, vkSubtract:
				state.adjustBackgroundAlpha(-overlayOpacityStep)
				return 0
			case vkF2:
				state.openSettings = true
				_, _, _ = procPostQuitMessage.Call(0)
				return 0
			}
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmPaint:
		var ps paintStruct
		_, _, _ = procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		_, _, _ = procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if state != nil {
			state.render()
		}
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	default:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	}
}

func (s *windowsOverlayState) render() {
	if s.hwnd == 0 {
		return
	}
	s.sizeMu.RLock()
	width, height := s.width, s.height
	s.sizeMu.RUnlock()
	if width < 240 {
		width = 240
	}
	if height < 220 {
		height = 220
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return
	}
	defer procDeleteDC.Call(memDC)

	bmi := bitmapInfo{}
	bmi.Header.Size = uint32(unsafe.Sizeof(bitmapInfoHeader{}))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = int32(-height) // top-down DIB
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = biRGB

	var bits uintptr
	hbmp, _, _ := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bmi)),
		uintptr(dibRGBColors),
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbmp == 0 || bits == 0 {
		return
	}
	defer procDeleteObject.Call(hbmp)
	oldObj, _, _ := procSelectObject.Call(memDC, hbmp)
	if oldObj != 0 {
		defer procSelectObject.Call(memDC, oldObj)
	}

	s.alphaMu.RLock()
	bgAlpha := s.bgAlpha
	s.alphaMu.RUnlock()
	bg := color.NRGBA{R: 15, G: 23, B: 42, A: bgAlpha}
	bgPixel := dibPixel(bg)
	pixels := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), width*height)
	for i := range pixels {
		pixels[i] = bgPixel
	}
	_, _, _ = procSetBkMode.Call(memDC, transparentBkMode)
	if font, _, _ := procGetStockObject.Call(stockDefaultGUIFont); font != 0 {
		_, _, _ = procSelectObject.Call(memDC, font)
	}
	s.drawContent(memDC)

	bgRGB := bgPixel & 0x00ffffff
	for i, px := range pixels {
		if px&0x00ffffff != bgRGB {
			pixels[i] = px | 0xff000000
		}
	}

	dstPt := s.windowTopLeft()
	size := winSize{CX: int32(width), CY: int32(height)}
	srcPt := winPoint{}
	blend := blendFunction{
		BlendOp:             acSrcOver,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}
	_, _, _ = procUpdateLayeredWindow.Call(
		s.hwnd,
		screenDC,
		uintptr(unsafe.Pointer(&dstPt)),
		uintptr(unsafe.Pointer(&size)),
		memDC,
		uintptr(unsafe.Pointer(&srcPt)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		uintptr(ulwAlpha),
	)
}

func (s *windowsOverlayState) windowTopLeft() winPoint {
	var rect winRect
	ret, _, _ := procGetWindowRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return winPoint{X: 60, Y: 60}
	}
	return winPoint{X: rect.Left, Y: rect.Top}
}

func (s *windowsOverlayState) drawContent(hdc uintptr) {
	s.displayMu.RLock()
	rows := append([]overlayRow(nil), s.displayRows...)
	lastUpdated := s.lastUpdated
	anySuccess := s.anySuccess
	s.displayMu.RUnlock()

	s.sizeMu.RLock()
	width, height := s.width, s.height
	s.sizeMu.RUnlock()
	const (
		rankX     = 8
		playerX   = 30
		headerY   = 52
		firstRowY = 78
		rowStepY  = 28
	)
	ratingX := width - 86
	if ratingX < 150 {
		ratingX = 150
	}
	maxRowsY := height - 50
	if maxRowsY < firstRowY {
		maxRowsY = firstRowY
	}
	playerGapX := 8
	nameMaxWidth := ratingX - playerX - playerGapX

	drawWinText(hdc, width/2-55, 8, "Score Monitor", colorRef(colorHeaderText))
	if anySuccess {
		drawWinText(hdc, width/2-78, 26, lastUpdated, colorRef(colorHeaderText))
	} else {
		drawWinText(hdc, width/2-45, 26, "updating...", colorRef(colorHeaderText))
	}
	drawWinText(hdc, 8, 40, fmt.Sprintf("BG Transparency %d%%  (+/-), Settings: F2/right-click", s.backgroundTransparencyPercent()), colorRef(colorHeaderText))
	drawWinText(hdc, rankX, headerY, "#", colorRef(colorHeaderText))
	drawWinText(hdc, playerX, headerY, fitWinTextToWidth(hdc, "PLAYER", nameMaxWidth), colorRef(colorHeaderText))
	drawWinText(hdc, ratingX, headerY, "RATING", colorRef(colorHeaderText))

	y := firstRowY
	for _, row := range rows {
		rankColor := colorRef(colorMuted)
		nameColor := colorRef(colorText)
		ratingColor := colorRef(colorText)
		if row.isError {
			nameColor = colorRef(colorMuted)
			ratingColor = colorRef(colorMuted)
		} else if score, err := strconv.Atoi(row.rating); err == nil {
			ratingColor = colorRef(ratingColorByScore(score, colorText))
		}
		drawWinText(hdc, rankX, y, row.rank, rankColor)
		drawWinText(hdc, playerX, y, fitWinTextToWidth(hdc, row.name, nameMaxWidth), nameColor)
		drawWinText(hdc, ratingX, y, row.rating, ratingColor)
		y += rowStepY
		if y > maxRowsY {
			break
		}
	}
}

func (s *windowsOverlayState) hitSettings(x, y int) bool {
	s.sizeMu.RLock()
	width, height := s.width, s.height
	s.sizeMu.RUnlock()
	btn := settingsButtonRect(width, height)
	return x >= btn.x && x <= btn.x+btn.w && y >= btn.y && y <= btn.y+btn.h
}

type winButtonRect struct {
	x int
	y int
	w int
	h int
}

func settingsButtonRect(width, height int) winButtonRect {
	if width < 120 {
		width = 120
	}
	if height < 90 {
		height = 90
	}
	return winButtonRect{x: width - 96, y: height - 38, w: 88, h: 26}
}

func fillDIBRect(pixels []uint32, width int, height int, rect winButtonRect, c color.NRGBA) {
	if width <= 0 || height <= 0 {
		return
	}
	x0 := rect.x
	y0 := rect.y
	x1 := rect.x + rect.w
	y1 := rect.y + rect.h
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > width {
		x1 = width
	}
	if y1 > height {
		y1 = height
	}
	if x0 >= x1 || y0 >= y1 {
		return
	}
	px := dibPixel(c)
	for y := y0; y < y1; y++ {
		row := y * width
		for x := x0; x < x1; x++ {
			pixels[row+x] = px
		}
	}
}

func fitWinTextToWidth(hdc uintptr, raw string, maxWidth int) string {
	if raw == "" || maxWidth <= 0 {
		return ""
	}
	if measureWinTextWidth(hdc, raw) <= maxWidth {
		return raw
	}
	runes := []rune(raw)
	for n := len(runes) - 1; n >= 1; n-- {
		candidate := string(runes[:n])
		if measureWinTextWidth(hdc, candidate) <= maxWidth {
			return candidate
		}
	}
	return ""
}

func measureWinTextWidth(hdc uintptr, text string) int {
	if text == "" {
		return 0
	}
	w, err := syscall.UTF16FromString(text)
	if err != nil || len(w) == 0 {
		return 0
	}
	var sz winSize
	_, _, _ = procGetTextExtentPoint32W.Call(
		hdc,
		uintptr(unsafe.Pointer(&w[0])),
		uintptr(len([]rune(text))),
		uintptr(unsafe.Pointer(&sz)),
	)
	return int(sz.CX)
}

func drawWinText(hdc uintptr, x, y int, text string, colorRefValue uint32) {
	if text == "" {
		return
	}
	w, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procSetTextColor.Call(hdc, uintptr(colorRefValue))
	_, _, _ = procTextOutW.Call(
		hdc,
		uintptr(int32(x)),
		uintptr(int32(y)),
		uintptr(unsafe.Pointer(w)),
		uintptr(len([]rune(text))),
	)
}

func colorRef(c color.NRGBA) uint32 {
	return uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16
}

func dibPixel(c color.NRGBA) uint32 {
	return uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}
