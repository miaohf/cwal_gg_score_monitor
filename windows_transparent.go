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
	wmNCHitTest = 0x0084
	wmKeyDown   = 0x0100
	htCaption   = 2

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsExLayered = 0x00080000
	wsExTopmost = 0x00000008
	wsPopup     = 0x80000000
	wsVisible   = 0x10000000

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

	biRGB        = 0
	dibRGBColors = 0
	ulwAlpha     = 0x00000002
	acSrcOver    = 0
	acSrcAlpha   = 1

	minOverlayBackgroundAlpha = 0
	maxOverlayBackgroundAlpha = 255
	overlayOpacityStep        = 15
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

	alphaMu sync.RWMutex
	bgAlpha uint8

	displayMu   sync.RWMutex
	displayRows []overlayRow
	lastUpdated string
	anySuccess  bool
}

type fynePreferences interface {
	Bool(string) bool
	Int(string) int
	SetBool(string, bool)
	SetInt(string, int)
}

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	gdi32                     = syscall.NewLazyDLL("gdi32.dll")
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	msimg32                   = syscall.NewLazyDLL("msimg32.dll")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
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
	state := &windowsOverlayState{
		rows:    rows,
		cfg:     cfg,
		prefs:   prefs,
		stopCh:  make(chan struct{}),
		bgAlpha: initialAlpha,
	}
	if err := state.run(); err != nil {
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
		uintptr(wsPopup|wsVisible),
		uintptr(60), uintptr(60), uintptr(420), uintptr(700),
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
	return nil
}

func (s *windowsOverlayState) backgroundTransparencyPercent() int {
	s.alphaMu.RLock()
	alpha := s.bgAlpha
	s.alphaMu.RUnlock()
	return int(transparencyPercentFromAlpha(alpha))
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
		return htCaption
	case wmKeyDown:
		if state != nil {
			switch wParam {
			case vkUp, vkOemPlus, vkAdd:
				state.adjustBackgroundAlpha(overlayOpacityStep)
				return 0
			case vkDown, vkOemMinus, vkSubtract:
				state.adjustBackgroundAlpha(-overlayOpacityStep)
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
	const width, height = 420, 700

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
	bmi.Header.Width = width
	bmi.Header.Height = -height // top-down DIB
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

	dstPt := winPoint{X: 60, Y: 60}
	size := winSize{CX: width, CY: height}
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

func (s *windowsOverlayState) drawContent(hdc uintptr) {
	s.displayMu.RLock()
	rows := append([]overlayRow(nil), s.displayRows...)
	lastUpdated := s.lastUpdated
	anySuccess := s.anySuccess
	s.displayMu.RUnlock()

	const (
		rankX      = 8
		playerX    = 30
		ratingX    = 260
		playerGapX = 8
		headerY    = 52
		firstRowY  = 78
		rowStepY   = 28
		maxRowsY   = 670
	)
	nameMaxWidth := ratingX - playerX - playerGapX

	drawWinText(hdc, 150, 8, "Score Monitor", colorRef(colorHeaderText))
	if anySuccess {
		drawWinText(hdc, 120, 26, lastUpdated, colorRef(colorHeaderText))
	} else {
		drawWinText(hdc, 160, 26, "updating...", colorRef(colorHeaderText))
	}
	drawWinText(hdc, 8, 40, fmt.Sprintf("BG Transparency %d%%  (+/-)", s.backgroundTransparencyPercent()), colorRef(colorHeaderText))
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
