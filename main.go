package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	readmePath             = "README.md"
	usersCSVPath           = "users.csv"
	fetchInterval          = 5 * time.Second
	requestTimeout         = 12 * time.Second
	updateRequestWait      = 1200 * time.Millisecond
	profileRetryInterval   = 700 * time.Millisecond
	profileRetryMaxAttempt = 4
	updateAPIURL           = "https://v2.api.cwal.gg/player-update"

	flashDuration = 1200 * time.Millisecond

	// Column widths – kept tight so that every column stays visible when the
	// window is resized narrow. The middle PLAYER column always takes the
	// remaining space via Border layout.
	colRankWidth   = 26
	colArrowWidth  = 14
	colRatingWidth = 56
	rowHeight      = 26
	headerHeight   = 18

	prefFontSizeKey        = "ui.font_size"
	prefFontColorRKey      = "ui.font_color_r"
	prefFontColorGKey      = "ui.font_color_g"
	prefFontColorBKey      = "ui.font_color_b"
	prefFontColorAKey      = "ui.font_color_a"
	prefFontTypeKey        = "ui.font_type"
	prefWindowOpacityKey   = "ui.window_opacity"
	prefSettingsSavedKey   = "ui.settings_saved"
	defaultWindowOpacity   = 0
	defaultFontSize        = 13
	defaultFontType        = "Regular"
)

var (
	colorRed        = color.NRGBA{R: 220, G: 38, B: 38, A: 255}
	colorGreen      = color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	colorUp         = color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	colorDown       = color.NRGBA{R: 239, G: 68, B: 68, A: 255}
	colorMuted      = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	colorText       = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	colorHeaderText = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
)

type player struct {
	Name   string
	CwalID string
}

type playerState struct {
	player
	LiveScore int
	LastError string

	hasPrev   bool
	prevScore int
	trend     int // -1 down, 0 same, 1 up
}

type apiConfig struct {
	ProfileURLTemplate string
	APIKey             string
	Authorization      string
}

type rowUI struct {
	rankText   *canvas.Text
	arrowText  *canvas.Text
	nameText   *canvas.Text
	ratingText *canvas.Text
	container  *fyne.Container
}

type headerUI struct {
	rank   *canvas.Text
	player *canvas.Text
	rating *canvas.Text
}

type uiSettings struct {
	mu              sync.RWMutex
	FontSize        float32
	FontColor       color.NRGBA
	FontType        string
	BackgroundAlpha uint8
}

type uiSettingsSnapshot struct {
	FontSize        float32
	FontColor       color.NRGBA
	FontType        string
	BackgroundAlpha uint8
}

func defaultUISettings() *uiSettings {
	return &uiSettings{
		FontSize:        defaultFontSize,
		FontColor:       color.NRGBA{R: 255, G: 255, B: 255, A: 255},
		FontType:        defaultFontType,
		BackgroundAlpha: defaultWindowOpacity,
	}
}

func loadUISettingsFromPrefs(p fyne.Preferences) *uiSettings {
	s := defaultUISettings()
	if !p.Bool(prefSettingsSavedKey) {
		return s
	}
	size := float32(p.Float(prefFontSizeKey))
	if size >= 10 && size <= 36 {
		s.FontSize = size
	}

	ft := p.String(prefFontTypeKey)
	if ft == "Regular" || ft == "Bold" || ft == "Monospace" {
		s.FontType = ft
	}

	r := clampByte(p.Int(prefFontColorRKey), int(s.FontColor.R))
	g := clampByte(p.Int(prefFontColorGKey), int(s.FontColor.G))
	b := clampByte(p.Int(prefFontColorBKey), int(s.FontColor.B))
	a := clampByte(p.Int(prefFontColorAKey), int(s.FontColor.A))
	s.FontColor = color.NRGBA{R: r, G: g, B: b, A: a}

	s.BackgroundAlpha = clampByte(p.Int(prefWindowOpacityKey), defaultWindowOpacity)
	return s
}

func saveUISettingsToPrefs(p fyne.Preferences, s uiSettingsSnapshot) {
	p.SetBool(prefSettingsSavedKey, true)
	p.SetFloat(prefFontSizeKey, float64(s.FontSize))
	p.SetString(prefFontTypeKey, s.FontType)
	p.SetInt(prefFontColorRKey, int(s.FontColor.R))
	p.SetInt(prefFontColorGKey, int(s.FontColor.G))
	p.SetInt(prefFontColorBKey, int(s.FontColor.B))
	p.SetInt(prefFontColorAKey, int(s.FontColor.A))
	p.SetInt(prefWindowOpacityKey, int(s.BackgroundAlpha))
}

func clampByte(v int, fallback int) uint8 {
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

func alphaFromTransparencyPercent(p uint8) uint8 {
	if p > 100 {
		p = 100
	}
	// 0% transparent -> alpha 255 (opaque)
	// 100% transparent -> alpha 0 (fully transparent)
	return uint8((100 - int(p)) * 255 / 100)
}

func transparencyPercentFromAlpha(a uint8) uint8 {
	// alpha 255 (opaque) -> 0% transparent
	// alpha 0 (fully transparent) -> 100% transparent
	return uint8((255 - int(a)) * 100 / 255)
}

func (s *uiSettings) Snapshot() uiSettingsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uiSettingsSnapshot{
		FontSize:        s.FontSize,
		FontColor:       s.FontColor,
		FontType:        s.FontType,
		BackgroundAlpha: s.BackgroundAlpha,
	}
}

func (s *uiSettings) Update(next uiSettingsSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FontSize = next.FontSize
	s.FontColor = next.FontColor
	s.FontType = next.FontType
	s.BackgroundAlpha = next.BackgroundAlpha
}

func main() {
	players, err := loadPlayersFromCSV(usersCSVPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load players: %v\n", err)
		os.Exit(1)
	}
	cfg, err := loadAPIConfigFromReadme(readmePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load API config: %v\n", err)
		os.Exit(1)
	}

	rows := make([]*playerState, 0, len(players))
	for _, p := range players {
		rows = append(rows, &playerState{
			player:    p,
			LastError: "pending",
		})
	}

	myApp := app.NewWithID("cwalgg.score.monitor")
	win := myApp.NewWindow("Score Monitor")
	win.Resize(fyne.NewSize(420, 700))
	settings := loadUISettingsFromPrefs(myApp.Preferences())

	// ---- Header ----
	statusDot := canvas.NewCircle(colorRed)
	statusDotBox := container.NewGridWrap(fyne.NewSize(10, 10), statusDot)
	updatedText := canvas.NewText("", colorHeaderText)
	updatedText.TextSize = 11

	leftPane := container.NewHBox(container.NewCenter(statusDotBox), updatedText)
	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), nil)
	settingsBtn.Importance = widget.LowImportance
	settingsBtnBox := container.NewGridWrap(fyne.NewSize(24, 24), settingsBtn)

	headerBar := container.NewBorder(nil, nil, leftPane, settingsBtnBox)
	headerPadded := container.NewPadded(headerBar)

	// ---- Column header ----
	colHeader, headerRefs := buildHeaderRow()

	// ---- Rows ----
	rowUIs := make([]*rowUI, len(rows))
	for i := range rows {
		rowUIs[i] = buildRowUI()
	}

	listVBox := container.NewVBox()
	for _, ru := range rowUIs {
		listVBox.Add(ru.container)
	}
	scroll := container.NewVScroll(listVBox)

	// ---- Footer ----
	footer := canvas.NewText(fmt.Sprintf("%d players  •  source: cwal.gg", len(rows)), colorHeaderText)
	footer.TextSize = 11
	footerBox := container.New(layout.NewCenterLayout(), footer)

	backgroundRect := canvas.NewRectangle(color.NRGBA{R: 15, G: 23, B: 42, A: settings.Snapshot().BackgroundAlpha})
	settingsBtn.OnTapped = func() {
		showFontSettingsDialog(
			win,
			settings,
			myApp.Preferences(),
			backgroundRect,
			[]*canvas.Text{updatedText, footer},
			headerRefs,
			rowUIs,
		)
	}

	content := container.NewBorder(
		container.NewVBox(
			headerPadded,
			widget.NewSeparator(),
			colHeader,
			widget.NewSeparator(),
		),
		container.NewVBox(
			widget.NewSeparator(),
			footerBox,
		),
		nil,
		nil,
		scroll,
	)
	applyTypography(settings.Snapshot(), []*canvas.Text{updatedText, footer}, headerRefs, rowUIs)
	win.SetContent(container.NewMax(backgroundRect, content))

	var rowsMu sync.RWMutex
	stopCh := make(chan struct{})
	go pollLoop(rows, rowUIs, listVBox, &rowsMu, statusDot, updatedText, stopCh, cfg, settings)
	win.SetCloseIntercept(func() {
		close(stopCh)
		win.Close()
	})

	win.ShowAndRun()
}

func buildHeaderRow() (*fyne.Container, headerUI) {
	rank := canvas.NewText("#", colorHeaderText)
	rank.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	rank.TextSize = 11
	rank.Alignment = fyne.TextAlignLeading

	player := canvas.NewText("PLAYER", colorHeaderText)
	player.TextStyle = fyne.TextStyle{Bold: true}
	player.TextSize = 11

	rating := canvas.NewText("RATING", colorHeaderText)
	rating.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	rating.TextSize = 11
	rating.Alignment = fyne.TextAlignTrailing

	rankBox := container.NewGridWrap(fyne.NewSize(colRankWidth, headerHeight), rank)
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, headerHeight), rating)

	return container.NewBorder(nil, nil, rankBox, ratingBox, player), headerUI{
		rank:   rank,
		player: player,
		rating: rating,
	}
}

func buildRowUI() *rowUI {
	rank := canvas.NewText("", colorMuted)
	rank.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rank.TextSize = 13
	rank.Alignment = fyne.TextAlignLeading

	arrow := canvas.NewText(" ", colorMuted)
	arrow.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	arrow.TextSize = 13

	name := canvas.NewText("", colorText)
	name.TextSize = 13

	rating := canvas.NewText("", colorText)
	rating.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rating.TextSize = 13
	rating.Alignment = fyne.TextAlignTrailing

	rankBox := container.NewGridWrap(fyne.NewSize(colRankWidth, rowHeight), rank)
	arrowBox := container.NewGridWrap(fyne.NewSize(colArrowWidth, rowHeight), arrow)
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, rowHeight), rating)

	playerBox := container.NewHBox(arrowBox, name)

	row := container.NewBorder(nil, nil, rankBox, ratingBox, playerBox)

	return &rowUI{
		rankText:   rank,
		arrowText:  arrow,
		nameText:   name,
		ratingText: rating,
		container:  row,
	}
}

func showFontSettingsDialog(
	win fyne.Window,
	settings *uiSettings,
	prefs fyne.Preferences,
	backgroundRect *canvas.Rectangle,
	staticTexts []*canvas.Text,
	headerRefs headerUI,
	rowUIs []*rowUI,
) {
	current := settings.Snapshot()

	sizeEntry := widget.NewEntry()
	sizeEntry.SetText(fmt.Sprintf("%.0f", current.FontSize))

	colorOptions := []string{"White", "Light Gray", "Green", "Cyan", "Yellow", "Red"}
	colorSelect := widget.NewSelect(colorOptions, nil)
	colorSelect.SetSelected(colorNameFromValue(current.FontColor))

	typeOptions := []string{"Regular", "Bold", "Monospace"}
	typeSelect := widget.NewSelect(typeOptions, nil)
	typeSelect.SetSelected(current.FontType)

	currentTransparency := transparencyPercentFromAlpha(current.BackgroundAlpha)
	alphaLabel := widget.NewLabel(fmt.Sprintf("%d%%", int(currentTransparency)))
	alphaSlider := widget.NewSlider(0, 100)
	alphaSlider.Step = 1
	alphaSlider.SetValue(float64(currentTransparency))
	alphaSlider.OnChanged = func(v float64) {
		alphaLabel.SetText(fmt.Sprintf("%d%%", int(v)))
	}
	alphaRow := container.NewBorder(nil, nil, nil, alphaLabel, alphaSlider)

	items := []*widget.FormItem{
		widget.NewFormItem("Font Size", sizeEntry),
		widget.NewFormItem("Font Color", colorSelect),
		widget.NewFormItem("Font Type", typeSelect),
		widget.NewFormItem("Background Transparency", alphaRow),
	}

	dialog.NewForm("Settings", "", "", items, func(ok bool) {
		if !ok {
			return
		}
		sizeVal, err := strconv.ParseFloat(strings.TrimSpace(sizeEntry.Text), 32)
		if err != nil || sizeVal < 10 || sizeVal > 36 {
			dialog.ShowError(errors.New("font size must be between 10 and 36"), win)
			return
		}

		selectedColor := colorValueByName(colorSelect.Selected)
		selectedType := typeSelect.Selected
		if selectedType == "" {
			selectedType = "Regular"
		}
		next := uiSettingsSnapshot{
			FontSize:        float32(sizeVal),
			FontColor:       selectedColor,
			FontType:        selectedType,
			BackgroundAlpha: alphaFromTransparencyPercent(uint8(alphaSlider.Value)),
		}
		settings.Update(next)
		saveUISettingsToPrefs(prefs, next)
		applyTypography(next, staticTexts, headerRefs, rowUIs)
		backgroundRect.FillColor = color.NRGBA{R: 15, G: 23, B: 42, A: next.BackgroundAlpha}
		backgroundRect.Refresh()
	}, win).Show()
}

func applyTypography(s uiSettingsSnapshot, staticTexts []*canvas.Text, headerRefs headerUI, rowUIs []*rowUI) {
	bodySize := s.FontSize
	headerSize := maxFloat32(10, bodySize-2)
	footerSize := maxFloat32(10, bodySize-2)

	bodyStyle := styleByType(s.FontType)
	headerStyle := bodyStyle
	headerStyle.Bold = true

	for i, t := range staticTexts {
		t.Color = s.FontColor
		if i == len(staticTexts)-1 { // footer
			t.TextStyle = bodyStyle
			t.TextSize = footerSize
		} else {
			t.TextStyle = bodyStyle
			t.TextSize = headerSize
		}
		t.Refresh()
	}

	headerRefs.rank.Color = s.FontColor
	headerRefs.player.Color = s.FontColor
	headerRefs.rating.Color = s.FontColor
	headerRefs.rank.TextSize = headerSize
	headerRefs.player.TextSize = headerSize
	headerRefs.rating.TextSize = headerSize
	headerRefs.rank.TextStyle = headerStyle
	headerRefs.player.TextStyle = headerStyle
	headerRefs.rating.TextStyle = headerStyle
	headerRefs.rank.Refresh()
	headerRefs.player.Refresh()
	headerRefs.rating.Refresh()

	for _, row := range rowUIs {
		row.rankText.TextSize = bodySize
		row.nameText.TextSize = bodySize
		row.arrowText.TextSize = bodySize
		row.ratingText.TextSize = bodySize
		row.rankText.TextStyle = headerStyle
		row.nameText.TextStyle = bodyStyle
		row.arrowText.TextStyle = headerStyle
		row.ratingText.TextStyle = headerStyle
		row.rankText.Color = dimColor(s.FontColor, 0.62)
		row.nameText.Color = s.FontColor
		row.ratingText.Color = s.FontColor
		row.rankText.Refresh()
		row.nameText.Refresh()
		row.arrowText.Refresh()
		row.ratingText.Refresh()
	}
}

func styleByType(tp string) fyne.TextStyle {
	switch tp {
	case "Bold":
		return fyne.TextStyle{Bold: true}
	case "Monospace":
		return fyne.TextStyle{Monospace: true}
	default:
		return fyne.TextStyle{}
	}
}

func colorNameFromValue(c color.NRGBA) string {
	for _, name := range []string{"White", "Light Gray", "Green", "Cyan", "Yellow", "Red"} {
		if colorValueByName(name) == c {
			return name
		}
	}
	return "White"
}

func colorValueByName(name string) color.NRGBA {
	switch name {
	case "Light Gray":
		return color.NRGBA{R: 226, G: 232, B: 240, A: 255}
	case "Green":
		return color.NRGBA{R: 134, G: 239, B: 172, A: 255}
	case "Cyan":
		return color.NRGBA{R: 125, G: 211, B: 252, A: 255}
	case "Yellow":
		return color.NRGBA{R: 253, G: 224, B: 71, A: 255}
	case "Red":
		return color.NRGBA{R: 248, G: 113, B: 113, A: 255}
	default:
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
}

func dimColor(c color.NRGBA, ratio float32) color.NRGBA {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return color.NRGBA{
		R: uint8(float32(c.R) * ratio),
		G: uint8(float32(c.G) * ratio),
		B: uint8(float32(c.B) * ratio),
		A: c.A,
	}
}

func maxFloat32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func pollLoop(
	rows []*playerState,
	rowUIs []*rowUI,
	listVBox *fyne.Container,
	rowsMu *sync.RWMutex,
	statusDot *canvas.Circle,
	updatedText *canvas.Text,
	stopCh <-chan struct{},
	cfg apiConfig,
	settings *uiSettings,
) {
	setIdle := func(ts string) {
		anyOK := anyRowOK(rows, rowsMu)
		fyne.Do(func() {
			if anyOK {
				statusDot.FillColor = colorGreen
				if ts != "" {
					updatedText.Text = ts
				}
			} else {
				statusDot.FillColor = colorRed
				updatedText.Text = ""
			}
			statusDot.Refresh()
			updatedText.Refresh()
		})
	}

	runCycle := func() {
		refreshRows(rows, rowsMu, cfg)
		applySortAndRender(rows, rowUIs, listVBox, rowsMu, settings)
		setIdle(time.Now().Format("2006-01-02 15:04:05"))
	}

	runCycle()

	ticker := time.NewTicker(fetchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			runCycle()
		}
	}
}

func anyRowOK(rows []*playerState, rowsMu *sync.RWMutex) bool {
	rowsMu.RLock()
	defer rowsMu.RUnlock()
	for _, r := range rows {
		if r.LastError == "" {
			return true
		}
	}
	return false
}

func refreshRows(rows []*playerState, rowsMu *sync.RWMutex, cfg apiConfig) {
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func(r *playerState) {
			defer wg.Done()
			result, err := fetchPlayerProfile(r.CwalID, cfg)

			rowsMu.Lock()
			defer rowsMu.Unlock()
			if err != nil {
				r.LastError = err.Error()
				return
			}

			if r.hasPrev {
				switch {
				case result.Rating > r.prevScore:
					r.trend = 1
				case result.Rating < r.prevScore:
					r.trend = -1
				default:
					r.trend = 0
				}
			} else {
				r.trend = 0
			}

			r.prevScore = result.Rating
			r.hasPrev = true

			r.LiveScore = result.Rating
			r.LastError = ""
		}(row)
	}
	wg.Wait()
}

// applySortAndRender sorts rows by rating desc, updates each rowUI's text/colors,
// reorders the list VBox, and triggers flash animation for rows whose rating changed.
func applySortAndRender(rows []*playerState, rowUIs []*rowUI, listVBox *fyne.Container, rowsMu *sync.RWMutex, settings *uiSettings) {
	uiSnap := settings.Snapshot()
	baseColor := uiSnap.FontColor
	mutedColor := dimColor(baseColor, 0.62)

	rowsMu.RLock()
	indices := make([]int, len(rows))
	for i := range rows {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		ra, rb := rows[indices[a]], rows[indices[b]]
		// rows with errors are pushed to bottom
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

	type flashTarget struct {
		text *canvas.Text
		from color.NRGBA
	}
	var flashes []flashTarget

	// Snapshot values for UI update under fyne thread later
	type rowSnapshot struct {
		ui      *rowUI
		rank    string
		arrow   string
		arrowC  color.NRGBA
		name    string
		nameC   color.NRGBA
		rating  string
		ratingC color.NRGBA
		trend   int
	}
	snapshots := make([]rowSnapshot, len(indices))

	for pos, idx := range indices {
		r := rows[idx]
		ui := rowUIs[idx]

		rankStr := strconv.Itoa(pos + 1)
		arrowStr := " "
		arrowC := mutedColor

		if r.LastError == "" {
			switch r.trend {
			case 1:
				arrowStr = "▲"
				arrowC = colorUp
			case -1:
				arrowStr = "▼"
				arrowC = colorDown
			}
		}

		ratingStr := "—"
		ratingC := mutedColor
		nameC := baseColor
		if r.LastError == "" {
			ratingStr = strconv.Itoa(r.LiveScore)
			ratingC = baseColor
		} else {
			nameC = mutedColor
			rankStr = "—"
		}

		snapshots[pos] = rowSnapshot{
			ui:      ui,
			rank:    rankStr,
			arrow:   arrowStr,
			arrowC:  arrowC,
			name:    r.Name,
			nameC:   nameC,
			rating:  ratingStr,
			ratingC: ratingC,
			trend:   r.trend,
		}

		if r.LastError == "" && r.trend != 0 {
			flashFrom := colorUp
			if r.trend == -1 {
				flashFrom = colorDown
			}
			flashes = append(flashes, flashTarget{text: ui.ratingText, from: flashFrom})
		}
	}
	rowsMu.RUnlock()

	fyne.Do(func() {
		// Apply text updates
		for _, s := range snapshots {
			s.ui.rankText.Text = s.rank
			s.ui.rankText.Refresh()

			s.ui.arrowText.Text = s.arrow
			s.ui.arrowText.Color = s.arrowC
			s.ui.arrowText.Refresh()

			s.ui.nameText.Text = s.name
			s.ui.nameText.Color = s.nameC
			s.ui.nameText.Refresh()

			s.ui.ratingText.Text = s.rating
			s.ui.ratingText.Color = s.ratingC
			s.ui.ratingText.Refresh()
		}

		// Reorder VBox children
		newObjects := make([]fyne.CanvasObject, 0, len(snapshots))
		for _, s := range snapshots {
			newObjects = append(newObjects, s.ui.container)
		}
		listVBox.Objects = newObjects
		listVBox.Refresh()

		// Start flash animations
		for _, f := range flashes {
			startColorFlash(f.text, f.from, baseColor)
		}
	})
}

func startColorFlash(txt *canvas.Text, from color.NRGBA, to color.NRGBA) {
	anim := &fyne.Animation{
		Duration: flashDuration,
		Tick: func(pct float32) {
			txt.Color = lerpNRGBA(from, to, pct)
			txt.Refresh()
		},
	}
	anim.Start()
}

func lerpNRGBA(a, b color.NRGBA, t float32) color.NRGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.NRGBA{
		R: uint8(float32(a.R) + (float32(b.R)-float32(a.R))*t),
		G: uint8(float32(a.G) + (float32(b.G)-float32(a.G))*t),
		B: uint8(float32(a.B) + (float32(b.B)-float32(a.B))*t),
		A: uint8(float32(a.A) + (float32(b.A)-float32(a.A))*t),
	}
}

type profileResult struct {
	Rating int
	Rank   string
}

func fetchPlayerProfile(cwalID string, cfg apiConfig) (profileResult, error) {
	if err := triggerPlayerUpdate(cwalID, cfg); err != nil {
		return profileResult{}, fmt.Errorf("trigger update failed: %w", err)
	}
	time.Sleep(updateRequestWait)

	var lastErr error
	for attempt := 1; attempt <= profileRetryMaxAttempt; attempt++ {
		result, err := queryProfile(cwalID, cfg)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt < profileRetryMaxAttempt {
			time.Sleep(profileRetryInterval)
		}
	}
	return profileResult{}, fmt.Errorf("query profile failed: %w", lastErr)
}

func triggerPlayerUpdate(cwalID string, cfg apiConfig) error {
	payload, err := json.Marshal(map[string]any{
		"gateway": 30,
		"alias":   cwalID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, updateAPIURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	if cfg.APIKey != "" {
		req.Header.Set("apikey", cfg.APIKey)
	}
	if cfg.Authorization != "" {
		req.Header.Set("Authorization", cfg.Authorization)
	}

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateText(string(body), 120))
	}
	return nil
}

func queryProfile(cwalID string, cfg apiConfig) (profileResult, error) {
	if cfg.ProfileURLTemplate == "" {
		return profileResult{}, errors.New("api_url not configured in README")
	}
	escapedID := url.QueryEscape(cwalID)
	targetURL := strings.Replace(cfg.ProfileURLTemplate, "{cwal_gg_id}", escapedID, 1)

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return profileResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	if cfg.APIKey != "" {
		req.Header.Set("apikey", cfg.APIKey)
	}
	if cfg.Authorization != "" {
		req.Header.Set("Authorization", cfg.Authorization)
	}

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return profileResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return profileResult{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateText(string(body), 120))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return profileResult{}, err
	}
	return parseProfileFromJSON(body)
}

func parseProfileFromJSON(body []byte) (profileResult, error) {
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err == nil && len(list) > 0 {
		return parseProfileFromMap(list[0], body)
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil && len(obj) > 0 {
		return parseProfileFromMap(obj, body)
	}
	return profileResult{}, fmt.Errorf("invalid JSON: %s", truncateText(string(body), 180))
}

func parseProfileFromMap(obj map[string]any, rawBody []byte) (profileResult, error) {
	rawRating, ok := obj["rating"]
	if !ok {
		return profileResult{}, fmt.Errorf("missing rating: %s", truncateText(string(rawBody), 180))
	}
	rating, ok := toInt(rawRating)
	if !ok {
		return profileResult{}, fmt.Errorf("rating is not a number: %v", rawRating)
	}

	rank := ""
	if rawRank, ok := obj["rank"]; ok {
		if s, ok := rawRank.(string); ok {
			rank = strings.TrimSpace(s)
		}
	}
	return profileResult{Rating: rating, Rank: rank}, nil
}

func toInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func loadPlayersFromCSV(path string) ([]player, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(bufio.NewReader(file))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) <= 1 {
		return nil, errors.New("users.csv is empty or missing rows")
	}

	players := make([]player, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue
		}
		if len(rec) < 2 {
			continue
		}
		name := strings.TrimSpace(rec[0])
		cwalID := strings.TrimSpace(rec[1])
		if name == "" || cwalID == "" {
			continue
		}
		players = append(players, player{Name: name, CwalID: cwalID})
	}
	if len(players) == 0 {
		return nil, errors.New("no players parsed from users.csv (need two columns: name,cwal_gg_id)")
	}
	return players, nil
}

func loadAPIConfigFromReadme(path string) (apiConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return apiConfig{}, err
	}
	defer file.Close()

	cfg := apiConfig{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "https://") && strings.Contains(line, "player_profile_view") {
			cfg.ProfileURLTemplate = line
			continue
		}
		if strings.EqualFold(line, "Apikey:") {
			if scanner.Scan() {
				cfg.APIKey = strings.TrimSpace(scanner.Text())
			}
			continue
		}
		if strings.EqualFold(line, "Authorization:") {
			if scanner.Scan() {
				cfg.Authorization = strings.TrimSpace(scanner.Text())
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return apiConfig{}, err
	}
	if cfg.ProfileURLTemplate == "" {
		return apiConfig{}, errors.New("README missing player_profile_view URL")
	}
	if cfg.APIKey == "" {
		return apiConfig{}, errors.New("README missing Apikey")
	}
	if cfg.Authorization == "" {
		return apiConfig{}, errors.New("README missing Authorization")
	}
	return cfg, nil
}
