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
	wmDestroy       = 0x0002
	wmPaint         = 0x000F
	wmSize          = 0x0005
	wmMove          = 0x0003
	wmNCHitTest     = 0x0084
	wmKeyDown       = 0x0100
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmCommand       = 0x0111
	wmClose         = 0x0010
	htClient        = 1
	htCaption       = 2
	htLeft          = 10
	htRight         = 11
	htTop           = 12
	htTopLeft       = 13
	htTopRight      = 14
	htBottom        = 15
	htBottomLeft    = 16
	htBottomRight   = 17

	csHRedraw = 0x0002
	csVRedraw = 0x0001
	csDblClks = 0x0008

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
	minOverlayBackgroundAlpha = 16
	maxOverlayBackgroundAlpha = 255
	overlayOpacityStep        = 15

	defaultOverlayX      = 60
	defaultOverlayY      = 60
	defaultOverlayWidth  = 420
	defaultOverlayHeight = 700
	minOverlayWidth      = 128
	minOverlayHeight     = 220

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

	manualControlScore        = 1201
	manualControlStatus       = 1202
	manualControlSaveButton   = 1203
	manualControlCancelButton = 1204

	overlayRankX      = 8
	overlayPlayerX    = 30
	overlayHeaderY    = 72
	overlayFirstRowY  = 98
	overlayRowStepY   = 28
	overlayTextHeight = 16
	overlayStatusSize = 7
	overlayStatusGap  = 2
	overlayBadgeSize  = 10
	overlayResizeGrip = 8
	overlayDragTop    = 8
	overlayDragBottom = 34

	fontWeightNormal = 400
	fontWeightBold   = 700
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
	sourceIndex int
	badgeRank   int
	rank        string
	name        string
	rating      string
	updateOK    bool
	isError     bool
}

type windowsOverlayState struct {
	rows   []*playerState
	rowsMu sync.RWMutex
	cfg    apiConfig
	app    fyne.App
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

	rowStatusMu sync.RWMutex
	rowStatusOK map[int]bool

	settingsWnd      uintptr
	settingsControls windowsSettingsControls

	manualScoreWnd      uintptr
	manualScoreControls windowsManualScoreControls
	manualScoreRowIndex int
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

type windowsManualScoreControls struct {
	score  uintptr
	status uintptr
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
	procClientToScreen        = user32.NewProc("ClientToScreen")
	procUpdateLayeredWindow   = user32.NewProc("UpdateLayeredWindow")
	procCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC              = gdi32.NewProc("DeleteDC")
	procCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procGetStockObject        = gdi32.NewProc("GetStockObject")
	procCreateFontW           = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procEllipse               = gdi32.NewProc("Ellipse")
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
	myApp := app.NewWithID("cwalgg.score.monitor")
	prefs := myApp.Preferences()
	if !loadSettingsFileIntoPrefs(prefs) {
		saveUISettingsToPrefs(prefs, defaultUISettings().Snapshot())
	}
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
		rows:                rows,
		cfg:                 cfg,
		app:                 myApp,
		prefs:               prefs,
		ui:                  ui,
		poll:                poll,
		stopCh:              make(chan struct{}),
		x:                   placement.x,
		y:                   placement.y,
		width:               placement.width,
		height:              placement.height,
		bgAlpha:             initialAlpha,
		rowStatusOK:         make(map[int]bool, len(rows)),
		manualScoreRowIndex: -1,
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
		Style:     csHRedraw | csVRedraw | csDblClks,
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

func (s *windowsOverlayState) showManualScoreForPoint(y int) {
	y = s.overlayYFromClientY(y)
	if y < overlayFirstRowY {
		return
	}
	displayIndex := (y - overlayFirstRowY) / overlayRowStepY
	s.displayMu.RLock()
	if displayIndex < 0 || displayIndex >= len(s.displayRows) {
		s.displayMu.RUnlock()
		return
	}
	sourceIndex := s.displayRows[displayIndex].sourceIndex
	s.displayMu.RUnlock()
	s.showManualScore(sourceIndex)
}

func (s *windowsOverlayState) overlayYFromClientY(clientY int) int {
	if s.hwnd == 0 {
		return clientY
	}
	clientOrigin := winPoint{}
	if ret, _, _ := procClientToScreen.Call(s.hwnd, uintptr(unsafe.Pointer(&clientOrigin))); ret == 0 {
		return clientY
	}
	var rect winRect
	if ret, _, _ := procGetWindowRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rect))); ret == 0 {
		return clientY
	}
	return clientY + int(clientOrigin.Y-rect.Top)
}

func (s *windowsOverlayState) showManualScore(rowIndex int) {
	if rowIndex < 0 || rowIndex >= len(s.rows) {
		return
	}
	if s.manualScoreWnd != 0 {
		_, _, _ = procDestroyWindow.Call(s.manualScoreWnd)
	}
	s.rowsMu.RLock()
	row := s.rows[rowIndex]
	name := row.Name
	current := row.LiveScore
	s.rowsMu.RUnlock()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("CWALGGManualScoreWindow")
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		Style:     csHRedraw | csVRedraw,
		WndProc:   syscall.NewCallback(windowsSettingsWndProc),
		Instance:  hInstance,
		ClassName: className,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := syscall.UTF16PtrFromString("Manual Rating")
	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(wsExTopmost),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsOverlappedWindow|wsVisible),
		uintptr(s.x+40), uintptr(s.y+60), 340, 190,
		s.hwnd, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return
	}
	s.manualScoreWnd = hwnd
	s.manualScoreRowIndex = rowIndex
	s.populateManualScoreControls(hwnd, hInstance, name, current)
	_, _, _ = procShowWindow.Call(hwnd, sWShow)
	_, _, _ = procSetFocus.Call(s.manualScoreControls.score)
}

func (s *windowsOverlayState) populateManualScoreControls(hwnd, hInstance uintptr, name string, current int) {
	s.addStatic(hwnd, hInstance, 18, 20, 290, 22, fmt.Sprintf("%s score", name))
	value := ""
	if current > 0 {
		value = strconv.Itoa(current)
	}
	s.manualScoreControls.score = s.addEdit(hwnd, hInstance, manualControlScore, 18, 48, 290, 24, value)
	s.manualScoreControls.status = s.addStatic(hwnd, hInstance, 18, 82, 290, 22, "")
	s.addButton(hwnd, hInstance, manualControlSaveButton, 132, 116, 76, 28, "Save")
	s.addButton(hwnd, hInstance, manualControlCancelButton, 226, 116, 76, 28, "Cancel")
}

func (s *windowsOverlayState) closeManualScore() {
	if s.manualScoreWnd != 0 {
		_, _, _ = procDestroyWindow.Call(s.manualScoreWnd)
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
	} else {
		stopValue = defaultStopTime(time.Now()).Format("2006-01-02 15:04")
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
		logAPIFetch("pollLoop: runCycle starting")
		refreshRows(s.rows, &s.rowsMu, s.cfg)
		s.markRowUpdateStatuses()
		if s.prefs != nil {
			saveHistoryScoresToPrefs(s.prefs, s.rows, &s.rowsMu)
		}
		s.rebuildDisplayRows()
		s.render()
		logAPIFetch("pollLoop: runCycle completed")
	}
	s.rebuildDisplayRows()
	s.render()
	_, stopEnabledAtStart, stopAtStart, stoppedAtStart, _ := s.poll.Snapshot()
	if stopEnabledAtStart && !time.Now().Before(stopAtStart) {
		if !stoppedAtStart {
			s.poll.MarkStopped()
		}
		s.rebuildDisplayRows()
		s.render()
	}
	nextPollAt := nextScoreRefreshAt(time.Now())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cycleMu sync.Mutex
	cycleRunning := false
	logAPIFetch("pollLoop: started, nextPollAt=%v", nextPollAt)
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.pollResetCh():
			nextPollAt = nextScoreRefreshAt(time.Now())
			logAPIFetch("pollLoop: received Reset signal, nextPollAt=%v", nextPollAt)
			continue
		case <-s.pollKickCh():
			cycleMu.Lock()
			if cycleRunning {
				cycleMu.Unlock()
				continue
			}
			cycleRunning = true
			nextPollAt = nextScoreRefreshAt(time.Now())
			cycleMu.Unlock()
			logAPIFetch("pollLoop: received Kick signal")
			go func() {
				runCycle()
				cycleMu.Lock()
				cycleRunning = false
				cycleMu.Unlock()
			}()
			continue
		case <-ticker.C:
			_, stopEnabled, stopAt, stopped, _ := s.poll.Snapshot()
			now := time.Now()
			if stopped {
				continue
			}
			if stopEnabled && !now.Before(stopAt) {
				if s.poll.MarkStopped() {
					s.rebuildDisplayRows()
					s.render()
					logAPIFetch("pollLoop: polling stopped at stopAt=%v", stopAt)
				}
				continue
			}
			if now.Before(nextPollAt) {
				continue
			}
			cycleMu.Lock()
			if cycleRunning {
				cycleMu.Unlock()
				continue
			}
			cycleRunning = true
			nextPollAt = nextScoreRefreshAfter(now)
			cycleMu.Unlock()
			go func() {
				runCycle()
				cycleMu.Lock()
				cycleRunning = false
				cycleMu.Unlock()
			}()
		}
	}
}

func (s *windowsOverlayState) pollInterval() time.Duration {
	if s.poll == nil {
		return fetchInterval
	}
	interval, _, _, _, _ := s.poll.Snapshot()
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func (s *windowsOverlayState) pollStopped() bool {
	if s.poll == nil {
		return false
	}
	_, stopEnabled, stopAt, stopped, _ := s.poll.Snapshot()
	now := time.Now()
	logAPIFetch("pollStopped: stopEnabled=%v stopAt=%v stopped=%v now=%v", stopEnabled, stopAt, stopped, now)
	if stopped {
		logAPIFetch("pollStopped: returning true (already marked as stopped)")
		return true
	}
	if stopEnabled && !now.Before(stopAt) {
		logAPIFetch("pollStopped: stopAt has passed, marking as stopped")
		if s.poll.MarkStopped() {
			s.rebuildDisplayRows()
			s.render()
		}
		return true
	}
	logAPIFetch("pollStopped: returning false (polling active)")
	return false
}

func (s *windowsOverlayState) pollResetCh() <-chan struct{} {
	if s.poll == nil {
		return nil
	}
	return s.poll.resetCh
}

func (s *windowsOverlayState) pollKickCh() <-chan struct{} {
	if s.poll == nil {
		return nil
	}
	return s.poll.kickCh
}

func (s *windowsOverlayState) markRowUpdateStatuses() {
	s.rowsMu.RLock()
	s.rowStatusMu.Lock()
	for idx, row := range s.rows {
		s.rowStatusOK[idx] = row.LastUpdateOK
	}
	s.rowStatusMu.Unlock()
	s.rowsMu.RUnlock()
}

func (s *windowsOverlayState) rebuildDisplayRows() {
	s.rowsMu.RLock()
	showBadges := shouldShowBadges(s.poll)
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
		s.rowStatusMu.RLock()
		statusOK := s.rowStatusOK[idx]
		s.rowStatusMu.RUnlock()
		item := overlayRow{
			sourceIndex: idx,
			badgeRank:   -1,
			rank:        strconv.Itoa(pos + 1),
			name:        r.Name,
			rating:      strconv.Itoa(r.LiveScore),
			updateOK:    !showBadges && statusOK,
			isError:     r.LastError != "",
		}
		if item.isError {
			item.rank = "-"
			item.rating = "-"
		} else {
			anyOK = true
			if showBadges && pos < 3 {
				item.badgeRank = pos
			}
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
		if state != nil {
			return uintptr(state.hitTest(lParam))
		}
		return htClient
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
	case wmLButtonDblClk:
		if state != nil {
			y := int((lParam >> 16) & 0xffff)
			state.showManualScoreForPoint(y)
			return 0
		}
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
				state.closeManualScore()
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
		if state != nil {
			state.persistCurrentPreferences()
		}
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
			if hwnd == state.settingsWnd {
				switch id {
				case settingsControlSaveButton:
					state.applyWindowsSettings()
					return 0
				case settingsControlCancelButton:
					state.closeSettings()
					return 0
				}
			}
			if hwnd == state.manualScoreWnd {
				switch id {
				case manualControlSaveButton:
					state.applyManualScore()
					return 0
				case manualControlCancelButton:
					state.closeManualScore()
					return 0
				}
			}
		}
		if notify == cbnSelChange {
			return 0
		}
	case wmClose:
		if state != nil {
			if hwnd == state.settingsWnd {
				state.closeSettings()
				return 0
			}
			if hwnd == state.manualScoreWnd {
				state.closeManualScore()
				return 0
			}
		}
	case wmDestroy:
		if state != nil && state.settingsWnd == hwnd {
			state.settingsWnd = 0
			state.settingsControls = windowsSettingsControls{}
		} else if state != nil && state.manualScoreWnd == hwnd {
			state.manualScoreWnd = 0
			state.manualScoreControls = windowsManualScoreControls{}
			state.manualScoreRowIndex = -1
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
	logAPIFetch("applyWindowsSettings: stopEnabled=%v stopAt=%v", nextStopEnabled, nextStopAt)

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
		logAPIFetch("applyWindowsSettings: calling Update and Reset")
		s.poll.Update(nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
		s.poll.Reset()
	}
	if s.prefs != nil {
		savePollSettingsToPrefs(s.prefs, nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
		saveSettingsFileFromPrefs(s.prefs)
	}
	s.applyManualHoldToActiveRows(nextManualHold)
	s.alphaMu.Lock()
	s.bgAlpha = nextAlpha
	s.alphaMu.Unlock()
	s.render()
	s.closeSettings()
}

func (s *windowsOverlayState) applyManualHoldToActiveRows(manualHold time.Duration) {
	now := time.Now()
	changed := false
	s.rowsMu.Lock()
	for _, row := range s.rows {
		if !row.hasManual {
			continue
		}
		changed = true
		if manualHold > 0 {
			row.manualUntil = now.Add(manualHold)
		} else {
			row.hasManual = false
			row.manualUntil = time.Time{}
		}
	}
	s.rowsMu.Unlock()
	if !changed {
		return
	}
	if manualHold > 0 && s.poll != nil {
		s.poll.KickAfter(manualHold)
	} else if s.poll != nil {
		s.poll.Kick()
	}
}

func (s *windowsOverlayState) persistCurrentPreferences() {
	if s.prefs == nil {
		return
	}
	if s.ui != nil {
		saveUISettingsToPrefs(s.prefs, s.ui.Snapshot())
	}
	if s.poll != nil {
		interval, stopEnabled, stopAt, _, manualHold := s.poll.Snapshot()
		savePollSettingsToPrefs(s.prefs, interval, stopEnabled, stopAt, manualHold)
	}
	s.saveWindowPlacement()
	saveHistoryScoresToPrefs(s.prefs, s.rows, &s.rowsMu)
	saveSettingsFileFromPrefs(s.prefs)
}

func (s *windowsOverlayState) applyManualScore() {
	rowIndex := s.manualScoreRowIndex
	if rowIndex < 0 || rowIndex >= len(s.rows) {
		s.closeManualScore()
		return
	}
	v, err := strconv.Atoi(strings.TrimSpace(getWindowText(s.manualScoreControls.score)))
	if err != nil || v < 0 || v > 4000 {
		setWindowText(s.manualScoreControls.status, "Score must be 0-4000")
		return
	}
	manualHold := manualHoldDefaultDuration
	if s.poll != nil {
		_, _, _, _, manualHold = s.poll.Snapshot()
	}

	s.rowsMu.Lock()
	row := s.rows[rowIndex]
	row.LiveScore = v
	row.LastError = ""
	row.LastUpdateOK = false
	row.hasManual = manualHold > 0
	manualUntil := time.Time{}
	if manualHold > 0 {
		manualUntil = time.Now().Add(manualHold)
		row.manualUntil = manualUntil
	} else {
		row.manualUntil = time.Time{}
	}
	row.prevScore = v
	row.hasPrev = true
	row.trend = 0
	s.rowsMu.Unlock()

	if s.prefs != nil {
		saveHistoryScoresToPrefs(s.prefs, s.rows, &s.rowsMu)
	}
	s.rebuildDisplayRows()
	s.render()
	if s.poll != nil {
		s.poll.Kick()
		s.kickWhenManualHoldExpires(rowIndex, manualUntil)
	}
	s.closeManualScore()
}

func (s *windowsOverlayState) kickWhenManualHoldExpires(rowIndex int, manualUntil time.Time) {
	if manualUntil.IsZero() {
		return
	}
	go func() {
		timer := time.NewTimer(time.Until(manualUntil))
		defer timer.Stop()
		select {
		case <-s.stopCh:
			return
		case <-timer.C:
		}
		s.rowsMu.RLock()
		stillHeld := rowIndex >= 0 &&
			rowIndex < len(s.rows) &&
			s.rows[rowIndex].hasManual &&
			s.rows[rowIndex].manualUntil.Equal(manualUntil)
		s.rowsMu.RUnlock()
		if stillHeld && s.poll != nil {
			s.poll.Kick()
		}
	}()
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

func signedLowWord(v uintptr) int {
	w := int16(v & 0xffff)
	return int(w)
}

func signedHighWord(v uintptr) int {
	w := int16((v >> 16) & 0xffff)
	return int(w)
}

func (s *windowsOverlayState) hitTest(lParam uintptr) int {
	if s.hwnd == 0 {
		return htClient
	}
	var rect winRect
	if ret, _, _ := procGetWindowRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rect))); ret == 0 {
		return htClient
	}
	x := signedLowWord(lParam)
	y := signedHighWord(lParam)
	left := x < int(rect.Left)+overlayResizeGrip
	right := x >= int(rect.Right)-overlayResizeGrip
	top := y < int(rect.Top)+overlayResizeGrip
	bottom := y >= int(rect.Bottom)-overlayResizeGrip

	switch {
	case top && left:
		return htTopLeft
	case top && right:
		return htTopRight
	case bottom && left:
		return htBottomLeft
	case bottom && right:
		return htBottomRight
	case left:
		return htLeft
	case right:
		return htRight
	case top:
		return htTop
	case bottom:
		return htBottom
	}

	localY := y - int(rect.Top)
	if localY >= overlayDragTop && localY <= overlayDragBottom {
		return htCaption
	}
	return htClient
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
		oldFont, _, _ := procSelectObject.Call(memDC, font)
		if oldFont != 0 {
			defer procSelectObject.Call(memDC, oldFont)
		}
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

func (s *windowsOverlayState) createContentFont() uintptr {
	snap := defaultUISettings().Snapshot()
	if s.ui != nil {
		snap = s.ui.Snapshot()
	}
	height := int(snap.FontSize + 0.5)
	if height < 8 {
		height = 8
	}
	weight := fontWeightNormal
	if snap.FontType == "Bold" {
		weight = fontWeightBold
	}
	faceName := "Segoe UI"
	if snap.FontType == "Monospace" {
		faceName = "Consolas"
	}
	face, _ := syscall.UTF16PtrFromString(faceName)
	font, _, _ := procCreateFontW.Call(
		uintptr(int32(-height)),
		0,
		0,
		0,
		uintptr(weight),
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(face)),
	)
	return font
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
	if width < minOverlayWidth {
		width = minOverlayWidth
	}
	if height < minOverlayHeight {
		height = minOverlayHeight
	}
	drawCenteredWinText(hdc, 0, width, 8, "Score Monitor", colorRef(colorHeaderText))
	if anySuccess {
		drawCenteredWinText(hdc, 0, width, 26, fitWinTextToWidth(hdc, lastUpdated, width), colorRef(colorHeaderText))
	} else {
		drawCenteredWinText(hdc, 0, width, 26, "updating...", colorRef(colorHeaderText))
	}
	drawWinText(hdc, 8, 40, fmt.Sprintf("BG %d%% (+/-)", s.backgroundTransparencyPercent()), colorRef(colorHeaderText))

	contentFont := s.createContentFont()
	var oldFont uintptr
	if contentFont != 0 {
		oldFont, _, _ = procSelectObject.Call(hdc, contentFont)
	}
	ratingRightPad := 4
	ratingWidth := measureWinTextWidth(hdc, "RATING")
	if scoreWidth := measureWinTextWidth(hdc, "0000"); scoreWidth > ratingWidth {
		ratingWidth = scoreWidth
	}
	ratingX := width - ratingRightPad - ratingWidth
	if ratingX < 82 {
		ratingX = 82
	}
	maxRowsY := height - 50
	if maxRowsY < overlayFirstRowY {
		maxRowsY = overlayFirstRowY
	}
	playerGapX := 2
	indicatorSize := overlayStatusSize
	for _, row := range rows {
		if row.badgeRank >= 0 && overlayBadgeSize > indicatorSize {
			indicatorSize = overlayBadgeSize
			break
		}
	}
	indicatorX := ratingX - indicatorSize - overlayStatusGap
	nameMaxWidth := indicatorX - overlayPlayerX - playerGapX
	if nameMaxWidth < 8 {
		nameMaxWidth = 8
	}
	textColor := colorText
	if s.ui != nil {
		textColor = s.ui.Snapshot().FontColor
	}

	drawWinText(hdc, overlayRankX, overlayHeaderY, "#", colorRef(textColor))
	drawWinText(hdc, overlayPlayerX, overlayHeaderY, fitWinTextToWidth(hdc, "PLAYER", nameMaxWidth), colorRef(textColor))
	drawWinText(hdc, ratingX, overlayHeaderY, fitWinTextToWidth(hdc, "RATING", width-ratingX-ratingRightPad), colorRef(textColor))

	y := overlayFirstRowY
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
		drawWinText(hdc, overlayRankX, y, row.rank, rankColor)
		drawWinText(hdc, overlayPlayerX, y, fitWinTextToWidth(hdc, row.name, nameMaxWidth), nameColor)
		if row.badgeRank >= 0 {
			drawWinBadge(hdc, indicatorX+(indicatorSize-overlayBadgeSize)/2, y+(overlayRowStepY-overlayBadgeSize)/2, row.badgeRank)
		} else if row.updateOK {
			drawWinStatusDot(hdc, indicatorX+(indicatorSize-overlayStatusSize)/2, y+(overlayRowStepY-overlayStatusSize)/2)
		}
		drawWinText(hdc, ratingX, y, row.rating, ratingColor)
		y += overlayRowStepY
		if y > maxRowsY {
			break
		}
	}
	if oldFont != 0 {
		_, _, _ = procSelectObject.Call(hdc, oldFont)
	}
	if contentFont != 0 {
		_, _, _ = procDeleteObject.Call(contentFont)
	}

	// 在窗口底部显示日志文件提示
	footerY := height - 16
	if footerY > y+10 {
		drawCenteredWinText(hdc, 0, width, footerY, "Log: api_fetch.log  |  F2: Settings", colorRef(colorHeaderText))
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

func drawCenteredWinText(hdc uintptr, left, right, y int, text string, colorRefValue uint32) {
	if text == "" || right <= left {
		return
	}
	textWidth := measureWinTextWidth(hdc, text)
	x := left + (right-left-textWidth)/2
	if x < left {
		x = left
	}
	drawWinText(hdc, x, y, text, colorRefValue)
}

func drawWinStatusDot(hdc uintptr, x, y int) {
	drawWinCircle(hdc, x, y, overlayStatusSize, colorGreen)
}

func drawWinBadge(hdc uintptr, x, y int, rank int) {
	drawWinCircle(hdc, x, y, overlayBadgeSize, badgeColorByRank(rank))
}

func drawWinCircle(hdc uintptr, x, y, size int, c color.NRGBA) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorRef(c)))
	if brush == 0 {
		return
	}
	defer procDeleteObject.Call(brush)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	if oldBrush != 0 {
		defer procSelectObject.Call(hdc, oldBrush)
	}
	_, _, _ = procEllipse.Call(
		hdc,
		uintptr(int32(x)),
		uintptr(int32(y)),
		uintptr(int32(x+size)),
		uintptr(int32(y+size)),
	)
}

func badgeColorByRank(rank int) color.NRGBA {
	switch rank {
	case 0:
		return color.NRGBA{R: 234, G: 179, B: 8, A: 255}
	case 1:
		return color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	case 2:
		return color.NRGBA{R: 180, G: 83, B: 9, A: 255}
	default:
		return color.NRGBA{}
	}
}

func colorRef(c color.NRGBA) uint32 {
	return uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16
}

func dibPixel(c color.NRGBA) uint32 {
	return uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}
