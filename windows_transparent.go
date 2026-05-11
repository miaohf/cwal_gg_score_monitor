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

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

const (
	wmDestroy    = 0x0002
	wmEraseBkgnd = 0x0014
	wmPaint      = 0x000F
	wmNCHitTest  = 0x0084
	wmKeyDown    = 0x0100
	htCaption    = 2

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExLayered    = 0x00080000
	wsPopup        = 0x80000000
	wsVisible      = 0x10000000

	sWShow = 5

	lwaColorKey = 0x00000001
	lwaAlpha    = 0x00000002

	stockDefaultGUIFont = 17
	transparentBkMode   = 1
	idcArrow            = 32512

	vkUp       = 0x26
	vkDown     = 0x28
	vkOemPlus  = 0xBB
	vkOemMinus = 0xBD
	vkAdd      = 0x6B
	vkSubtract = 0x6D

	defaultOverlayOpacityAlpha = 230
	minOverlayOpacityAlpha     = 80
	maxOverlayOpacityAlpha     = 255
	overlayOpacityStep         = 15
)

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
	Pt      struct {
		X int32
		Y int32
	}
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

type winSize struct {
	CX int32
	CY int32
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
	prefs  fyne.Preferences
	hwnd   uintptr
	stopCh chan struct{}

	opacityMu sync.RWMutex
	opacityA  uint8

	displayMu   sync.RWMutex
	displayRows []overlayRow
	lastUpdated string
	anySuccess  bool
}

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	gdi32                     = syscall.NewLazyDLL("gdi32.dll")
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
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
	procFillRect              = user32.NewProc("FillRect")
	procInvalidateRect        = user32.NewProc("InvalidateRect")
	procSetLayeredWindowAttrs = user32.NewProc("SetLayeredWindowAttributes")
	procLoadCursorW           = user32.NewProc("LoadCursorW")
	procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procTextOutW              = gdi32.NewProc("TextOutW")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procGetStockObject        = gdi32.NewProc("GetStockObject")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")

	globalWindowsOverlayWindow *windowsOverlayState
)

func runWindowsTransparentMode(rows []*playerState, cfg apiConfig) bool {
	if err := initAPILogger(apiLogPath); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init api logger: %v\n", err)
	}
	prefs := app.NewWithID("cwalgg.score.monitor").Preferences()
	initialOpacity := uint8(defaultOverlayOpacityAlpha)
	if prefs.Bool(prefSettingsSavedKey) {
		initialOpacity = clampByte(prefs.Int(prefWindowOpacityKey), defaultOverlayOpacityAlpha)
		if initialOpacity < minOverlayOpacityAlpha {
			initialOpacity = minOverlayOpacityAlpha
		}
	}
	state := &windowsOverlayState{
		rows:     rows,
		cfg:      cfg,
		prefs:    prefs,
		stopCh:   make(chan struct{}),
		opacityA: initialOpacity,
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
		uintptr(wsExTopmost|wsExToolWindow|wsExLayered),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsPopup|wsVisible),
		uintptr(40), uintptr(40), uintptr(248), uintptr(700),
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW failed: %w", err)
	}
	s.hwnd = hwnd

	s.applyLayeredWindowAttributes()
	_, _, _ = procShowWindow.Call(hwnd, sWShow)
	_, _, _ = procUpdateWindow.Call(hwnd)

	go s.pollLoop()

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

func (s *windowsOverlayState) applyLayeredWindowAttributes() {
	if s.hwnd == 0 {
		return
	}
	s.opacityMu.RLock()
	alpha := s.opacityA
	s.opacityMu.RUnlock()
	_, _, _ = procSetLayeredWindowAttrs.Call(s.hwnd, 0x00000000, uintptr(alpha), uintptr(lwaColorKey|lwaAlpha))
}

func (s *windowsOverlayState) opacityPercent() int {
	s.opacityMu.RLock()
	alpha := s.opacityA
	s.opacityMu.RUnlock()
	return int(alpha) * 100 / 255
}

func (s *windowsOverlayState) adjustOpacity(delta int) {
	s.opacityMu.Lock()
	next := int(s.opacityA) + delta
	if next < minOverlayOpacityAlpha {
		next = minOverlayOpacityAlpha
	}
	if next > maxOverlayOpacityAlpha {
		next = maxOverlayOpacityAlpha
	}
	s.opacityA = uint8(next)
	nextAlpha := s.opacityA
	s.opacityMu.Unlock()

	if s.prefs != nil {
		s.prefs.SetBool(prefSettingsSavedKey, true)
		s.prefs.SetInt(prefWindowOpacityKey, int(nextAlpha))
	}
	s.applyLayeredWindowAttributes()
	if s.hwnd != 0 {
		_, _, _ = procInvalidateRect.Call(s.hwnd, 0, 1)
	}
}

func (s *windowsOverlayState) pollLoop() {
	runCycle := func() {
		refreshRows(s.rows, &s.rowsMu, s.cfg)
		s.rebuildDisplayRows()
		if s.hwnd != 0 {
			_, _, _ = procInvalidateRect.Call(s.hwnd, 0, 1)
		}
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
				state.adjustOpacity(overlayOpacityStep)
				return 0
			case vkDown, vkOemMinus, vkSubtract:
				state.adjustOpacity(-overlayOpacityStep)
				return 0
			}
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		if state != nil {
			state.paint(hwnd)
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

func (s *windowsOverlayState) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	blackBrush, _, _ := procCreateSolidBrush.Call(0x00000000)
	if blackBrush != 0 {
		_, _, _ = procFillRect.Call(hdc, uintptr(unsafe.Pointer(&ps.RcPaint)), blackBrush)
		_, _, _ = procDeleteObject.Call(blackBrush)
	}
	_, _, _ = procSetBkMode.Call(hdc, transparentBkMode)
	if font, _, _ := procGetStockObject.Call(stockDefaultGUIFont); font != 0 {
		_, _, _ = procSelectObject.Call(hdc, font)
	}

	s.displayMu.RLock()
	rows := append([]overlayRow(nil), s.displayRows...)
	lastUpdated := s.lastUpdated
	anySuccess := s.anySuccess
	s.displayMu.RUnlock()

	opacityPct := s.opacityPercent()
	const (
		rankX      = 8
		playerX    = 30
		ratingX    = 164
		headerY    = 52
		firstRowY  = 78
		rowStepY   = 28
		maxRowsY   = 670
		playerGapX = 8
	)
	nameMaxWidth := ratingX - playerX - playerGapX
	drawWinText(hdc, 88, 8, "Score Monitor", colorRef(colorHeaderText))
	if anySuccess {
		drawWinText(hdc, 62, 26, lastUpdated, colorRef(colorHeaderText))
	} else {
		drawWinText(hdc, 72, 26, "updating...", colorRef(colorHeaderText))
	}
	drawWinText(hdc, 8, 40, fmt.Sprintf("Opacity %d%%  (+/-)", opacityPct), colorRef(colorHeaderText))
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
