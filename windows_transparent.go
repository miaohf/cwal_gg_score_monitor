//go:build windows

package main

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
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
	wmCommand   = 0x0111
	wmClose     = 0x0010
	htCaption   = 2

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsExLayered        = 0x00080000
	wsExTopmost        = 0x00000008
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000

	esAutoHScroll   = 0x0080
	bsPushButton    = 0x00000000
	cbsDropDownList = 0x0003
	cbsAutoHScroll  = 0x0040

	cbnSelChange = 1
	bnClicked    = 0

	cbGetCurSel = 0x0147
	cbAddString = 0x0143
	cbSetCurSel = 0x014E

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
	vkEsc      = 0x1B

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
	minOverlayWidth      = 160
	minOverlayHeight     = 220

	prefWindowsOverlayXKey      = "windows_overlay.x"
	prefWindowsOverlayYKey      = "windows_overlay.y"
	prefWindowsOverlayWidthKey  = "windows_overlay.width"
	prefWindowsOverlayHeightKey = "windows_overlay.height"

	settingsControlFontSize     = 1001
	settingsControlFontColor    = 1002
	settingsControlFontType     = 1003
	settingsControlTransparency = 1004
	settingsControlPollInterval = 1005
	settingsControlManualHold   = 1006
	settingsControlStopTime     = 1007
	settingsControlStatus       = 1008
	settingsControlSaveButton   = 1101
	settingsControlCancelButton = 1102
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
	prefs  fyne.Preferences
	ui     *uiSettings
	poll   *pollControl
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

	settingsWnd      uintptr
	settingsControls windowsSettingsControls
}

type fynePreferences interface {
	Bool(string) bool
	Int(string) int
	SetBool(string, bool)
	SetInt(string, int)
}

type windowsSettingsControls struct {
	fontSize     uintptr
	fontColor    uintptr
	fontType     uintptr
	transparency uintptr
	pollInterval uintptr
	manualHold   uintptr
	stopTime     uintptr
	status       uintptr
}

type overlayPlacement struct {
	x      int
	y      int
	width  int
	height int
}

var (
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
	procSendMessageW          = user32.NewProc("SendMessageW")
	procSetWindowTextW        = user32.NewProc("SetWindowTextW")
	procGetWindowTextW        = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	procSetFocus              = user32.NewProc("SetFocus")
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
	ui := loadUISettingsFromPrefs(prefs)
	poll := loadPollSettingsFromPrefs(prefs)
	loadHistoryScoresFromPrefs(prefs, rows)
	initialAlpha := ui.Snapshot().BackgroundAlpha
	if initialAlpha < minOverlayBackgroundAlpha {
		initialAlpha = minOverlayBackgroundAlpha
		ui.Update(uiSettingsSnapshot{
			FontSize:        ui.Snapshot().FontSize,
			FontColor:       ui.Snapshot().FontColor,
			FontType:        ui.Snapshot().FontType,
			BackgroundAlpha: initialAlpha,
		})
	}
	placement := loadOverlayPlacement(prefs)
	state := &windowsOverlayState{
		rows:    rows,
		cfg:     cfg,
		prefs:   prefs,
		ui:      ui,
		poll:    poll,
		stopCh:  make(chan struct{}),
		x:       placement.x,
		y:       placement.y,
		width:   placement.width,
		height:  placement.height,
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
	if v := prefs.Int(prefWindowsOverlayWidthKey); v >= minOverlayWidth && v <= 4000 {
		p.width = v
	}
	if v := prefs.Int(prefWindowsOverlayHeightKey); v >= minOverlayHeight && v <= 4000 {
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
	if width < minOverlayWidth || height < minOverlayHeight {
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

	if s.ui != nil {
		next := s.ui.Snapshot()
		next.BackgroundAlpha = nextAlpha
		s.ui.Update(next)
	}
	if s.prefs != nil {
		s.prefs.SetBool(prefSettingsSavedKey, true)
		s.prefs.SetInt(prefWindowOpacityKey, int(nextAlpha))
	}
	s.render()
}

func (s *windowsOverlayState) showSettings() {
	if s.settingsWnd != 0 {
		_, _, _ = procShowWindow.Call(s.settingsWnd, sWShow)
		_, _, _ = procSetFocus.Call(s.settingsWnd)
		return
	}
	s.createSettingsWindow()
}

func (s *windowsOverlayState) closeSettings() {
	if s.settingsWnd != 0 {
		_, _, _ = procDestroyWindow.Call(s.settingsWnd)
	}
}

func (s *windowsOverlayState) createSettingsWindow() {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("CWALGGSettingsWindow")
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		Style:     csHRedraw | csVRedraw,
		WndProc:   syscall.NewCallback(windowsSettingsWndProc),
		Instance:  hInstance,
		ClassName: className,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := syscall.UTF16PtrFromString("Settings")
	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(wsExTopmost),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsOverlappedWindow|wsVisible),
		uintptr(s.x+30), uintptr(s.y+40), 360, 360,
		s.hwnd, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return
	}
	s.settingsWnd = hwnd
	s.populateSettingsControls(hwnd, hInstance)
	_, _, _ = procShowWindow.Call(hwnd, sWShow)
	_, _, _ = procSetFocus.Call(s.settingsControls.fontSize)
}

func (s *windowsOverlayState) populateSettingsControls(hwnd, hInstance uintptr) {
	current := defaultUISettings().Snapshot()
	if s.ui != nil {
		current = s.ui.Snapshot()
	}
	interval := fetchInterval
	stopEnabled := false
	stopAt := time.Time{}
	manualHold := manualHoldDefaultDuration
	if s.poll != nil {
		interval, stopEnabled, stopAt, _, manualHold = s.poll.Snapshot()
	}

	labelX, fieldX := 18, 138
	y := 18
	rowH := 32
	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Font Size")
	s.settingsControls.fontSize = s.addEdit(hwnd, hInstance, settingsControlFontSize, fieldX, y, 170, 24, fmt.Sprintf("%.0f", current.FontSize))
	y += rowH

	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Font Color")
	s.settingsControls.fontColor = s.addCombo(hwnd, hInstance, settingsControlFontColor, fieldX, y, 170, 120, []string{"White", "Light Gray", "Green", "Cyan", "Yellow", "Red"}, colorNameFromValue(current.FontColor))
	y += rowH

	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Font Type")
	s.settingsControls.fontType = s.addCombo(hwnd, hInstance, settingsControlFontType, fieldX, y, 170, 120, []string{"Regular", "Bold", "Monospace"}, current.FontType)
	y += rowH

	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "BG %")
	s.settingsControls.transparency = s.addEdit(hwnd, hInstance, settingsControlTransparency, fieldX, y, 170, 24, strconv.Itoa(int(transparencyPercentFromAlpha(current.BackgroundAlpha))))
	y += rowH

	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Poll(s)")
	s.settingsControls.pollInterval = s.addEdit(hwnd, hInstance, settingsControlPollInterval, fieldX, y, 170, 24, strconv.Itoa(int(interval/time.Second)))
	y += rowH

	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Hold(s)")
	s.settingsControls.manualHold = s.addEdit(hwnd, hInstance, settingsControlManualHold, fieldX, y, 170, 24, strconv.Itoa(int(manualHold/time.Second)))
	y += rowH

	stopValue := ""
	if stopEnabled {
		stopValue = stopAt.Format("2006-01-02 15:04")
	}
	s.addStatic(hwnd, hInstance, labelX, y+4, 110, 22, "Stop Time")
	s.settingsControls.stopTime = s.addEdit(hwnd, hInstance, settingsControlStopTime, fieldX, y, 170, 24, stopValue)
	y += rowH + 6

	s.settingsControls.status = s.addStatic(hwnd, hInstance, labelX, y, 300, 22, "")
	y += 34
	s.addButton(hwnd, hInstance, settingsControlSaveButton, fieldX, y, 76, 28, "Save")
	s.addButton(hwnd, hInstance, settingsControlCancelButton, fieldX+94, y, 76, 28, "Cancel")
}

func (s *windowsOverlayState) addStatic(parent, hInstance uintptr, x, y, w, h int, text string) uintptr {
	return createWinControl("STATIC", text, wsChild|wsVisible, parent, hInstance, 0, x, y, w, h)
}

func (s *windowsOverlayState) addEdit(parent, hInstance uintptr, id, x, y, w, h int, text string) uintptr {
	return createWinControl("EDIT", text, wsChild|wsVisible|wsTabStop|wsBorder|esAutoHScroll, parent, hInstance, id, x, y, w, h)
}

func (s *windowsOverlayState) addButton(parent, hInstance uintptr, id, x, y, w, h int, text string) uintptr {
	return createWinControl("BUTTON", text, wsChild|wsVisible|wsTabStop|bsPushButton, parent, hInstance, id, x, y, w, h)
}

func (s *windowsOverlayState) addCombo(parent, hInstance uintptr, id, x, y, w, h int, options []string, selected string) uintptr {
	hwnd := createWinControl("COMBOBOX", "", wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropDownList|cbsAutoHScroll, parent, hInstance, id, x, y, w, h)
	selectedIndex := 0
	for i, option := range options {
		sendComboString(hwnd, cbAddString, option)
		if option == selected {
			selectedIndex = i
		}
	}
	_, _, _ = procSendMessageW.Call(hwnd, cbSetCurSel, uintptr(selectedIndex), 0)
	return hwnd
}

func createWinControl(className, text string, style uintptr, parent, hInstance uintptr, id, x, y, w, h int) uintptr {
	classPtr, _ := syscall.UTF16PtrFromString(className)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), hInstance, 0,
	)
	return hwnd
}

func sendComboString(hwnd uintptr, msg uintptr, value string) {
	ptr, _ := syscall.UTF16PtrFromString(value)
	_, _, _ = procSendMessageW.Call(hwnd, msg, 0, uintptr(unsafe.Pointer(ptr)))
}

func (s *windowsOverlayState) pollLoop() {
	runCycle := func() {
		refreshRows(s.rows, &s.rowsMu, s.cfg)
		if s.prefs != nil {
			saveHistoryScoresToPrefs(s.prefs, s.rows, &s.rowsMu)
		}
		s.rebuildDisplayRows()
		s.render()
	}
	runCycle()
	for {
		interval := fetchInterval
		if s.poll != nil {
			nextInterval, stopEnabled, stopAt, stopped, _ := s.poll.Snapshot()
			if stopEnabled && time.Now().After(stopAt) {
				s.poll.MarkStopped()
				s.rebuildDisplayRows()
				s.render()
				return
			}
			if stopped {
				return
			}
			interval = nextInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-timer.C:
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
			state.showSettings()
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
				state.showSettings()
				return 0
			case vkEsc:
				state.closeSettings()
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

func windowsSettingsWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := globalWindowsOverlayWindow
	switch msg {
	case wmCommand:
		id := lowWord(wParam)
		notify := highWord(wParam)
		if state != nil && notify == bnClicked {
			switch id {
			case settingsControlSaveButton:
				state.applyWindowsSettings()
				return 0
			case settingsControlCancelButton:
				state.closeSettings()
				return 0
			}
		}
		if notify == cbnSelChange {
			return 0
		}
	case wmClose:
		if state != nil {
			state.closeSettings()
			return 0
		}
	case wmDestroy:
		if state != nil && state.settingsWnd == hwnd {
			state.settingsWnd = 0
			state.settingsControls = windowsSettingsControls{}
		}
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func (s *windowsOverlayState) applyWindowsSettings() {
	controls := s.settingsControls
	sizeVal, err := strconv.ParseFloat(strings.TrimSpace(getWindowText(controls.fontSize)), 32)
	if err != nil || sizeVal < 10 || sizeVal > 36 {
		setWindowText(controls.status, "Font size must be 10-36")
		return
	}
	transparency, err := strconv.Atoi(strings.TrimSpace(getWindowText(controls.transparency)))
	if err != nil || transparency < 0 || transparency > 100 {
		setWindowText(controls.status, "BG % must be 0-100")
		return
	}
	intervalSec, err := strconv.Atoi(strings.TrimSpace(getWindowText(controls.pollInterval)))
	if err != nil || intervalSec < 60 || intervalSec > 86400 {
		setWindowText(controls.status, "Poll(s) must be 60-86400")
		return
	}
	manualHoldSec, err := strconv.Atoi(strings.TrimSpace(getWindowText(controls.manualHold)))
	if err != nil || manualHoldSec < 0 || manualHoldSec > 86400 {
		setWindowText(controls.status, "Hold(s) must be 0-86400")
		return
	}

	stopText := strings.TrimSpace(getWindowText(controls.stopTime))
	nextStopEnabled := false
	nextStopAt := time.Time{}
	if stopText != "" {
		parsed, parseErr := time.ParseInLocation("2006-01-02 15:04", stopText, time.Now().Location())
		if parseErr != nil {
			setWindowText(controls.status, "Stop Time: YYYY-MM-DD HH:MM")
			return
		}
		if !parsed.After(time.Now()) {
			setWindowText(controls.status, "Stop Time must be later than now")
			return
		}
		nextStopEnabled = true
		nextStopAt = parsed
	}

	nextAlpha := alphaFromTransparencyPercent(uint8(transparency))
	if nextAlpha < minOverlayBackgroundAlpha {
		nextAlpha = minOverlayBackgroundAlpha
	}
	next := uiSettingsSnapshot{
		FontSize:        float32(sizeVal),
		FontColor:       colorValueByName(selectedComboText(controls.fontColor, []string{"White", "Light Gray", "Green", "Cyan", "Yellow", "Red"})),
		FontType:        selectedComboText(controls.fontType, []string{"Regular", "Bold", "Monospace"}),
		BackgroundAlpha: nextAlpha,
	}
	if next.FontType == "" {
		next.FontType = defaultFontType
	}
	if s.ui != nil {
		s.ui.Update(next)
	}
	if s.prefs != nil {
		saveUISettingsToPrefs(s.prefs, next)
	}

	nextInterval := time.Duration(intervalSec) * time.Second
	nextManualHold := time.Duration(manualHoldSec) * time.Second
	if s.poll != nil {
		s.poll.Update(nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
		s.poll.Reset()
	}
	if s.prefs != nil {
		savePollSettingsToPrefs(s.prefs, nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
	}
	s.alphaMu.Lock()
	s.bgAlpha = nextAlpha
	s.alphaMu.Unlock()
	s.render()
	s.closeSettings()
}

func selectedComboText(hwnd uintptr, options []string) string {
	idx, _, _ := procSendMessageW.Call(hwnd, cbGetCurSel, 0, 0)
	i := int(idx)
	if i < 0 || i >= len(options) {
		return ""
	}
	return options[i]
}

func getWindowText(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, int(n)+1)
	if len(buf) == 0 {
		return ""
	}
	ret, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:ret])
}

func setWindowText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	ptr, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(ptr)))
}

func lowWord(v uintptr) int {
	return int(v & 0xffff)
}

func highWord(v uintptr) int {
	return int((v >> 16) & 0xffff)
}

func (s *windowsOverlayState) render() {
	if s.hwnd == 0 {
		return
	}
	s.sizeMu.RLock()
	width, height := s.width, s.height
	s.sizeMu.RUnlock()
	if width < minOverlayWidth {
		width = minOverlayWidth
	}
	if height < minOverlayHeight {
		height = minOverlayHeight
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
		headerY   = 72
		firstRowY = 98
		rowStepY  = 28
	)
	ratingRightPad := 8
	ratingWidth := measureWinTextWidth(hdc, "RATING")
	if scoreWidth := measureWinTextWidth(hdc, "0000"); scoreWidth > ratingWidth {
		ratingWidth = scoreWidth
	}
	ratingX := width - ratingRightPad - ratingWidth
	if ratingX < 96 {
		ratingX = 96
	}
	maxRowsY := height - 50
	if maxRowsY < firstRowY {
		maxRowsY = firstRowY
	}
	playerGapX := 4
	nameMaxWidth := ratingX - playerX - playerGapX
	if nameMaxWidth < 20 {
		nameMaxWidth = 20
	}
	textColor := colorText
	if s.ui != nil {
		textColor = s.ui.Snapshot().FontColor
	}

	drawWinText(hdc, width/2-55, 8, "Score Monitor", colorRef(colorHeaderText))
	if anySuccess {
		drawWinText(hdc, width/2-78, 26, lastUpdated, colorRef(colorHeaderText))
	} else {
		drawWinText(hdc, width/2-45, 26, "updating...", colorRef(colorHeaderText))
	}
	drawWinText(hdc, 8, 40, fmt.Sprintf("BG %d%% (+/-)", s.backgroundTransparencyPercent()), colorRef(colorHeaderText))
	drawWinText(hdc, 8, 56, "F2/right-click settings", colorRef(colorHeaderText))
	drawWinText(hdc, rankX, headerY, "#", colorRef(colorHeaderText))
	drawWinText(hdc, playerX, headerY, fitWinTextToWidth(hdc, "PLAYER", nameMaxWidth), colorRef(colorHeaderText))
	drawWinText(hdc, ratingX, headerY, fitWinTextToWidth(hdc, "RATING", width-ratingX-ratingRightPad), colorRef(colorHeaderText))

	y := firstRowY
	for _, row := range rows {
		rankColor := colorRef(colorMuted)
		nameColor := colorRef(textColor)
		ratingColor := colorRef(textColor)
		if row.isError {
			nameColor = colorRef(colorMuted)
			ratingColor = colorRef(colorMuted)
		} else if score, err := strconv.Atoi(row.rating); err == nil {
			ratingColor = colorRef(ratingColorByScore(score, textColor))
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

type winButtonRect struct {
	x int
	y int
	w int
	h int
}

func (s *windowsOverlayState) drawSettingsPanel(hdc uintptr, width, height int) {
	panel := settingsPanelRect(width, height)
	x := panel.x
	y := panel.y

	// Text drawn after this rect becomes fully opaque in the post-processing pass.
	drawWinText(hdc, x+18, y+16, "Settings", colorRef(colorText))
	drawWinText(hdc, x+18, y+48, fmt.Sprintf("BG Transparency: %d%%", s.backgroundTransparencyPercent()), colorRef(colorText))
	drawWinText(hdc, x+18, y+78, "+ / Up: less transparent", colorRef(colorHeaderText))
	drawWinText(hdc, x+18, y+104, "- / Down: more transparent", colorRef(colorHeaderText))
	drawWinText(hdc, x+18, y+130, "F2 / right-click: toggle settings", colorRef(colorHeaderText))
	drawWinText(hdc, x+18, y+156, "Esc: close", colorRef(colorHeaderText))
}

func settingsPanelRect(width, height int) winButtonRect {
	panelW := width - 48
	if panelW > 380 {
		panelW = 380
	}
	if panelW < 240 {
		panelW = 240
	}
	panelH := 190
	x := (width - panelW) / 2
	y := (height - panelH) / 2
	if y < 80 {
		y = 80
	}
	return winButtonRect{x: x, y: y, w: panelW, h: panelH}
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
