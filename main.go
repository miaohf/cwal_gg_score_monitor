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
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/text/encoding/simplifiedchinese"
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
	apiLogPath             = "api_fetch.log"

	flashDuration = 1200 * time.Millisecond

	// Column widths – kept tight so that every column stays visible when the
	// window is resized narrow. Rank / badge / name sit in one row (CustomPaddedHBox);
	// rating + badge sit on the Border right with extra padding (ratingColPadRight).
	colRankWidth      = 22
	colBadgeWidth     = 16
	colRatingWidth    = 50
	ratingColPadRight float32 = 14 // gutter so scores don't hug the window edge
	rowHeight         = 26
	headerHeight      = 18
	listRowSpacing    float32 = 10 // gap between leaderboard rows for clearer stripes

	prefFontSizeKey        = "ui.font_size"
	prefFontColorRKey      = "ui.font_color_r"
	prefFontColorGKey      = "ui.font_color_g"
	prefFontColorBKey      = "ui.font_color_b"
	prefFontColorAKey      = "ui.font_color_a"
	prefFontTypeKey        = "ui.font_type"
	prefWindowOpacityKey   = "ui.window_opacity"
	prefSettingsSavedKey   = "ui.settings_saved"
	prefPollSettingsSaved  = "poll.settings_saved"
	prefPollIntervalSecKey = "poll.interval_sec"
	prefPollStopEnabledKey = "poll.stop_enabled"
	prefPollStopAtKey      = "poll.stop_at"
	prefHistoryScoresKey   = "history.scores_json"
	defaultWindowOpacity   = 0
	defaultFontSize        = 13
	defaultFontType        = "Regular"
	appVersion             = "v1.1.0"
)

var (
	colorRed        = color.NRGBA{R: 220, G: 38, B: 38, A: 255}
	colorGreen      = color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	// Ranking trend flashes (replacing arrows): red — score/mark up; green — score down.
	// «紫铜» mixed into the flash starter for a warm metallic hint.
	colorFlashCopperPurple  = color.NRGBA{R: 176, G: 124, B: 148, A: 255}
	colorFlashRankingUp   = color.NRGBA{R: 235, G: 72, B: 90, A: 255}  // rise — red (排名上升感)
	colorFlashRankingDown = color.NRGBA{R: 34, G: 197, B: 130, A: 255} // fall — green
	colorMuted      = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	colorText       = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	colorHeaderText = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	// Row panels: translucent milky overlays (real frosted blur is not supported by Fyne).
	colorRowGlass     = color.NRGBA{R: 241, G: 245, B: 249, A: 40} // default row frost
	colorTop8RowGlass = color.NRGBA{R: 226, G: 232, B: 240, A: 72} // slightly brighter strip for top 8
	colorRating2200 = color.NRGBA{R: 110, G: 178, B: 238, A: 255} // blue
	colorRating2300 = color.NRGBA{R: 152, G: 176, B: 234, A: 255} // blue-violet
	colorRating2400 = color.NRGBA{R: 188, G: 172, B: 232, A: 255} // violet

	badgeResourceNone = fyne.NewStaticResource("badge-none.svg", []byte(`
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <circle cx="8" cy="8" r="6.5" fill="#000000" fill-opacity="0"/>
</svg>`))
	badgeResourceChampion = fyne.NewStaticResource("badge-champion.svg", []byte(`
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <circle cx="8" cy="8" r="6.5" fill="#EAB308"/>
  <circle cx="8" cy="8" r="4.8" fill="#FDE047"/>
</svg>`))
	badgeResourceRunnerUp = fyne.NewStaticResource("badge-runner-up.svg", []byte(`
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <circle cx="8" cy="8" r="6.5" fill="#94A3B8"/>
  <circle cx="8" cy="8" r="4.8" fill="#E2E8F0"/>
</svg>`))
	badgeResourceThird = fyne.NewStaticResource("badge-third.svg", []byte(`
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <circle cx="8" cy="8" r="6.5" fill="#B45309"/>
  <circle cx="8" cy="8" r="4.8" fill="#F59E0B"/>
</svg>`))

	apiLoggerMu sync.RWMutex
	apiLogger   *log.Logger
)

type player struct {
	Name   string
	CwalID string
}

type playerState struct {
	player
	LiveScore int
	LastError string
	hasManual bool

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
	background *canvas.Rectangle
	rankText   *canvas.Text
	badgeIcon  *canvas.Image
	nameText   *canvas.Text
	ratingText *canvas.Text
	container  *fyne.Container
}

type pollControl struct {
	mu          sync.RWMutex
	interval    time.Duration
	stopEnabled bool
	stopAt      time.Time
	stopped     bool
}

type savedScore struct {
	CwalID string `json:"cwal_id"`
	Score  int    `json:"score"`
}

func newPollControl() *pollControl {
	return &pollControl{interval: fetchInterval}
}

func (p *pollControl) Snapshot() (time.Duration, bool, time.Time, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.interval, p.stopEnabled, p.stopAt, p.stopped
}

func (p *pollControl) Update(interval time.Duration, stopEnabled bool, stopAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval = interval
	p.stopEnabled = stopEnabled
	p.stopAt = stopAt
	p.stopped = false
}

func (p *pollControl) MarkStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return false
	}
	p.stopped = true
	return true
}

func shouldShowBadges(p *pollControl) bool {
	if p == nil {
		return false
	}
	_, stopEnabled, stopAt, stopped := p.Snapshot()
	if stopped {
		return true
	}
	if !stopEnabled {
		return false
	}
	return !time.Now().Before(stopAt)
}

func loadPollSettingsFromPrefs(p fyne.Preferences) *pollControl {
	pc := newPollControl()
	if !p.Bool(prefPollSettingsSaved) {
		return pc
	}

	intervalSec := p.Int(prefPollIntervalSecKey)
	if intervalSec < 1 || intervalSec > 3600 {
		intervalSec = int(fetchInterval / time.Second)
	}

	stopEnabled := p.Bool(prefPollStopEnabledKey)
	stopAt := time.Time{}
	if raw := strings.TrimSpace(p.String(prefPollStopAtKey)); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			stopAt = parsed
		}
	}
	// Backward/compat mode: if stop time exists, treat it as enabled.
	if !stopEnabled && !stopAt.IsZero() {
		stopEnabled = true
	}
	if stopEnabled && stopAt.IsZero() {
		stopEnabled = false
	}
	pc.Update(time.Duration(intervalSec)*time.Second, stopEnabled, stopAt)
	return pc
}

func savePollSettingsToPrefs(p fyne.Preferences, interval time.Duration, stopEnabled bool, stopAt time.Time) {
	p.SetBool(prefPollSettingsSaved, true)
	p.SetInt(prefPollIntervalSecKey, int(interval/time.Second))
	p.SetBool(prefPollStopEnabledKey, stopEnabled)
	if stopEnabled && !stopAt.IsZero() {
		p.SetString(prefPollStopAtKey, stopAt.Format(time.RFC3339))
	} else {
		p.SetString(prefPollStopAtKey, "")
	}
}

func loadHistoryScoresFromPrefs(p fyne.Preferences, rows []*playerState) {
	raw := strings.TrimSpace(p.String(prefHistoryScoresKey))
	if raw == "" {
		return
	}
	var items []savedScore
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return
	}
	scoreByID := make(map[string]int, len(items))
	for _, it := range items {
		if it.CwalID == "" {
			continue
		}
		scoreByID[it.CwalID] = it.Score
	}
	for _, r := range rows {
		if score, ok := scoreByID[r.CwalID]; ok {
			r.LiveScore = score
			r.prevScore = score
			r.hasPrev = true
			r.LastError = ""
			r.trend = 0
		}
	}
}

func saveHistoryScoresToPrefs(p fyne.Preferences, rows []*playerState, rowsMu *sync.RWMutex) {
	rowsMu.RLock()
	defer rowsMu.RUnlock()
	items := make([]savedScore, 0, len(rows))
	for _, r := range rows {
		if r.LastError != "" {
			continue
		}
		items = append(items, savedScore{
			CwalID: r.CwalID,
			Score:  r.LiveScore,
		})
	}
	b, err := json.Marshal(items)
	if err != nil {
		return
	}
	p.SetString(prefHistoryScoresKey, string(b))
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

type doubleTapBox struct {
	widget.BaseWidget
	content   fyne.CanvasObject
	onDouble  func()
	lastTapAt time.Time
}

func newDoubleTapBox(content fyne.CanvasObject, onDouble func()) *doubleTapBox {
	d := &doubleTapBox{content: content, onDouble: onDouble}
	d.ExtendBaseWidget(d)
	return d
}

func (d *doubleTapBox) Tapped(_ *fyne.PointEvent) {
	now := time.Now()
	if !d.lastTapAt.IsZero() && now.Sub(d.lastTapAt) <= 450*time.Millisecond && d.onDouble != nil {
		d.onDouble()
		d.lastTapAt = time.Time{}
		return
	}
	d.lastTapAt = now
}

func (d *doubleTapBox) TappedSecondary(_ *fyne.PointEvent) {}

func (d *doubleTapBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.content)
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
	if err := initAPILogger(apiLogPath); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init api logger: %v\n", err)
	}
	settings := loadUISettingsFromPrefs(myApp.Preferences())
	pollCfg := loadPollSettingsFromPrefs(myApp.Preferences())
	loadHistoryScoresFromPrefs(myApp.Preferences(), rows)

	// ---- Header (countdown top-right; status dot is in footer by last-updated) ----
	statusDot := canvas.NewCircle(colorRed)
	statusDotBox := container.NewGridWrap(fyne.NewSize(10, 10), statusDot)
	updatedText := canvas.NewText("", colorHeaderText)
	updatedText.TextSize = 10
	countdownText := canvas.NewText("--:--:--", colorHeaderText)
	countdownText.TextSize = 12
	countdownText.Alignment = fyne.TextAlignCenter
	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), nil)
	settingsBtn.Importance = widget.LowImportance
	settingsBtnBox := container.NewGridWrap(fyne.NewSize(20, 20), settingsBtn)
	logBtn := widget.NewButtonWithIcon("", theme.DocumentIcon(), nil)
	logBtn.Importance = widget.LowImportance
	logBtnBox := container.NewGridWrap(fyne.NewSize(20, 20), logBtn)

	headerBar := container.New(layout.NewCenterLayout(), countdownText)
	headerPadded := container.NewPadded(headerBar)

	// ---- Column header ----
	colHeader, headerRefs := buildHeaderRow()

	// ---- Rows ----
	rowUIs := make([]*rowUI, len(rows))
	var listVBox *fyne.Container
	var rowsMu sync.RWMutex
	for i := range rows {
		idx := i
		rowUIs[i] = buildRowUI(func() {
			showManualScoreDialog(win, rows[idx], &rowsMu, func() {
				saveHistoryScoresToPrefs(myApp.Preferences(), rows, &rowsMu)
				applySortAndRender(rows, rowUIs, listVBox, &rowsMu, settings, shouldShowBadges(pollCfg))
			})
		})
	}

	listVBox = container.New(layout.NewCustomPaddedVBoxLayout(listRowSpacing))
	for _, ru := range rowUIs {
		listVBox.Add(ru.container)
	}
	scroll := container.NewVScroll(listVBox)

	// ---- Footer ----
	footer := canvas.NewText(
		fmt.Sprintf("%dP • %s ", len(rows), appVersion),
		colorHeaderText,
	)
	footer.TextSize = 10
	leftFooterBtns := container.NewHBox(settingsBtnBox, logBtnBox)
	footerStatsRow := container.NewBorder(nil, nil, leftFooterBtns, nil, container.New(layout.NewCenterLayout(), footer))
	updatedRow := container.New(layout.NewCustomPaddedHBoxLayout(4),
		container.NewCenter(statusDotBox), updatedText)
	footerBox := container.NewVBox(footerStatsRow, updatedRow)

	backgroundRect := canvas.NewRectangle(color.NRGBA{R: 15, G: 23, B: 42, A: settings.Snapshot().BackgroundAlpha})
	settingsBtn.OnTapped = func() {
		showFontSettingsDialog(
			win,
			settings,
			myApp.Preferences(),
			pollCfg,
			backgroundRect,
			[]*canvas.Text{updatedText, countdownText, footer},
			headerRefs,
			rowUIs,
		)
	}
	logBtn.OnTapped = func() {
		showAPILogDialog(win, apiLogPath)
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
	applyTypography(settings.Snapshot(), []*canvas.Text{updatedText, countdownText, footer}, headerRefs, rowUIs)
	win.SetContent(container.NewMax(backgroundRect, content))

	stopCh := make(chan struct{})
	go pollLoop(rows, rowUIs, listVBox, &rowsMu, statusDot, updatedText, countdownText, stopCh, cfg, settings, pollCfg, myApp.Preferences(), win)
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
	player.Alignment = fyne.TextAlignLeading

	rating := canvas.NewText("RATING", colorHeaderText)
	rating.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	rating.TextSize = 11
	rating.Alignment = fyne.TextAlignCenter

	rankBox := container.NewGridWrap(fyne.NewSize(colRankWidth, headerHeight), rank)
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, headerHeight), rating)

	badgeHeaderSlot := canvas.NewRectangle(color.Transparent)
	titleHBox := container.New(layout.NewCustomPaddedHBoxLayout(2), rankBox, player)
	ratingHeaderCluster := container.New(layout.NewCustomPaddedHBoxLayout(2),
		container.NewGridWrap(fyne.NewSize(colBadgeWidth, headerHeight), badgeHeaderSlot),
		ratingBox,
	)
	ratingHeaderPadded := container.New(layout.NewCustomPaddedLayout(0, 0, 0, ratingColPadRight), ratingHeaderCluster)
	return container.NewBorder(nil, nil, nil, ratingHeaderPadded, titleHBox), headerUI{
		rank:   rank,
		player: player,
		rating: rating,
	}
}

func buildRowUI(onDoubleTap func()) *rowUI {
	bg := canvas.NewRectangle(color.Transparent)
	rank := canvas.NewText("", colorMuted)
	rank.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rank.TextSize = 13
	rank.Alignment = fyne.TextAlignLeading

	name := canvas.NewText("", colorText)
	name.TextSize = 13

	badge := canvas.NewImageFromResource(badgeResourceNone)
	badge.FillMode = canvas.ImageFillContain
	badge.SetMinSize(fyne.NewSize(16, 16))

	rating := canvas.NewText("", colorText)
	rating.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rating.TextSize = 13
	rating.Alignment = fyne.TextAlignTrailing

	rankBox := container.NewGridWrap(fyne.NewSize(colRankWidth, rowHeight), rank)
	badgeBox := container.NewGridWrap(fyne.NewSize(colBadgeWidth, rowHeight), container.NewCenter(badge))
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, rowHeight), rating)

	playerBox := container.New(layout.NewCustomPaddedHBoxLayout(1), rankBox, name)
	ratingCluster := container.New(layout.NewCustomPaddedHBoxLayout(2), badgeBox, ratingBox)
	ratingClusterPadded := container.New(layout.NewCustomPaddedLayout(0, 0, 0, ratingColPadRight), ratingCluster)

	rowContent := container.NewBorder(nil, nil, nil, ratingClusterPadded, playerBox)
	row := container.NewMax(bg, rowContent, newDoubleTapBox(rowContent, onDoubleTap))

	return &rowUI{
		background: bg,
		rankText:   rank,
		badgeIcon:  badge,
		nameText:   name,
		ratingText: rating,
		container:  row,
	}
}

func showFontSettingsDialog(
	win fyne.Window,
	settings *uiSettings,
	prefs fyne.Preferences,
	pollCfg *pollControl,
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
	intervalEntry := widget.NewEntry()
	interval, stopEnabled, stopAt, _ := pollCfg.Snapshot()
	intervalEntry.SetText(strconv.Itoa(int(interval / time.Second)))

	stopTimeEntry := widget.NewEntry()
	stopTimeEntry.SetPlaceHolder("YYYY-MM-DD HH:MM")
	if stopEnabled {
		stopTimeEntry.SetText(stopAt.Format("2006-01-02 15:04"))
	} else {
		now := time.Now()
		stopTimeEntry.SetText(fmt.Sprintf("%s 00:00", now.Format("2006-01-02")))
	}

	items := []*widget.FormItem{
		widget.NewFormItem("Font Size", sizeEntry),
		widget.NewFormItem("Font Color", colorSelect),
		widget.NewFormItem("Font Type", typeSelect),
		widget.NewFormItem("BG Transparency", alphaRow),
		widget.NewFormItem("Polling Interval(m)", intervalEntry),
		widget.NewFormItem("Stop Time", stopTimeEntry),
	}

	formDlg := dialog.NewForm("Settings", "", "", items, func(ok bool) {
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
		intervalSec, err := strconv.Atoi(strings.TrimSpace(intervalEntry.Text))
		if err != nil || intervalSec < 1 || intervalSec > 3600 {
			dialog.ShowError(errors.New("polling interval must be between 1 and 3600 seconds"), win)
			return
		}
		stopText := strings.TrimSpace(stopTimeEntry.Text)
		nextStopEnabled := false
		nextStopAt := time.Time{}
		if stopText != "" {
			parsed, parseErr := time.ParseInLocation("2006-01-02 15:04", stopText, time.Now().Location())
			if parseErr != nil {
				dialog.ShowError(errors.New("stop time format must be YYYY-MM-DD HH:MM"), win)
				return
			}
			if !parsed.After(time.Now()) {
				dialog.ShowError(errors.New("stop time must be later than now"), win)
				return
			}
			nextStopAt = parsed
			nextStopEnabled = true
		}
		settings.Update(next)
		saveUISettingsToPrefs(prefs, next)
		pollCfg.Update(time.Duration(intervalSec)*time.Second, nextStopEnabled, nextStopAt)
		savePollSettingsToPrefs(prefs, time.Duration(intervalSec)*time.Second, nextStopEnabled, nextStopAt)
		applyTypography(next, staticTexts, headerRefs, rowUIs)
		backgroundRect.FillColor = color.NRGBA{R: 15, G: 23, B: 42, A: next.BackgroundAlpha}
		backgroundRect.Refresh()
	}, win)
	sizeEntry.OnSubmitted = func(_ string) {
		formDlg.Submit()
	}
	intervalEntry.OnSubmitted = func(_ string) {
		formDlg.Submit()
	}
	stopTimeEntry.OnSubmitted = func(_ string) {
		formDlg.Submit()
	}
	formDlg.Show()
}

func applyTypography(s uiSettingsSnapshot, staticTexts []*canvas.Text, headerRefs headerUI, rowUIs []*rowUI) {
	bodySize := s.FontSize
	headerSize := maxFloat32(10, bodySize-2)
	footerSize := maxFloat32(9, bodySize-3)

	bodyStyle := styleByType(s.FontType)
	headerStyle := bodyStyle
	headerStyle.Bold = true

	for i, t := range staticTexts {
		t.Color = s.FontColor
		// [0]=last-updated (footer strip), [1]=countdown (header), [2]=footer stats
		if i == 1 {
			clockStyle := styleByType(s.FontType)
			clockStyle.Monospace = true
			t.TextStyle = clockStyle
			t.TextSize = bodySize // larger than surrounding header-strip text
		} else {
			t.TextStyle = bodyStyle
			t.TextSize = footerSize
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
		row.ratingText.TextSize = bodySize
		row.rankText.TextStyle = headerStyle
		row.nameText.TextStyle = bodyStyle
		row.ratingText.TextStyle = headerStyle
		row.rankText.Color = dimColor(s.FontColor, 0.62)
		row.nameText.Color = s.FontColor
		row.ratingText.Color = s.FontColor
		row.rankText.Refresh()
		row.nameText.Refresh()
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

func initAPILogger(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	apiLoggerMu.Lock()
	apiLogger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	apiLoggerMu.Unlock()
	return nil
}

func logAPIFetch(format string, args ...any) {
	apiLoggerMu.RLock()
	l := apiLogger
	apiLoggerMu.RUnlock()
	if l == nil {
		return
	}
	l.Printf(format, args...)
}

func showAPILogDialog(win fyne.Window, logPath string) {
	content, err := readLastLines(logPath, 100)
	if err != nil {
		dialog.ShowError(fmt.Errorf("failed to read log file: %w", err), win)
		return
	}
	if strings.TrimSpace(content) == "" {
		content = "(log is empty)"
	}
	grid := widget.NewTextGridFromString(content)
	grid.ShowLineNumbers = false
	applyWhiteStyleForGrid(grid, content)
	scroll := container.NewVScroll(grid)

	var d dialog.Dialog
	stopRefresh := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopRefresh)
		})
	}
	closeBtn := widget.NewButton("关闭", func() {
		stop()
		// Clear UI-held content on close to avoid retaining log text in memory.
		grid.SetText("")
		d.Hide()
	})
	body := container.NewBorder(nil, closeBtn, nil, nil, scroll)
	d = dialog.NewCustomWithoutButtons("API日志", body, win)
	d.SetOnClosed(func() {
		stop()
		// Ensure closed dialog does not keep large text content.
		grid.SetText("")
	})
	d.Resize(fyne.NewSize(760, 480))
	d.Show()
	fyne.Do(func() {
		scroll.ScrollToBottom()
	})

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastContent := content
		for {
			select {
			case <-stopRefresh:
				return
			case <-ticker.C:
				next, err := readLastLines(logPath, 100)
				if err != nil {
					continue
				}
				if strings.TrimSpace(next) == "" {
					next = "(log is empty)"
				}
				if next == lastContent {
					continue
				}
				lastContent = next
				fyne.Do(func() {
					grid.SetText(next)
					applyWhiteStyleForGrid(grid, next)
					scroll.ScrollToBottom()
				})
			}
		}
	}()
}

func applyWhiteStyleForGrid(grid *widget.TextGrid, content string) {
	lines := strings.Split(content, "\n")
	lastRow := len(lines) - 1
	lastCol := 0
	if lastRow >= 0 {
		lastCol = len([]rune(lines[lastRow]))
	}
	grid.SetStyleRange(0, 0, lastRow, lastCol, &widget.CustomTextGridStyle{
		FGColor: color.White,
		BGColor: color.Transparent,
	})
}

func readLastLines(path string, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 100
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	lines := make([]string, 0, maxLines)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if len(lines) == maxLines {
			copy(lines, lines[1:])
			lines[maxLines-1] = scanner.Text()
		} else {
			lines = append(lines, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

// formatDurationHMS renders a non‑negative duration as HH:MM:SS (for countdown display).
func formatDurationHMS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, (sec%3600)/60, sec%60)
}

func pollLoop(
	rows []*playerState,
	rowUIs []*rowUI,
	listVBox *fyne.Container,
	rowsMu *sync.RWMutex,
	statusDot *canvas.Circle,
	updatedText *canvas.Text,
	countdownText *canvas.Text,
	stopCh <-chan struct{},
	cfg apiConfig,
	settings *uiSettings,
	pollCfg *pollControl,
	prefs fyne.Preferences,
	win fyne.Window,
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
		saveHistoryScoresToPrefs(prefs, rows, rowsMu)
		applySortAndRender(rows, rowUIs, listVBox, rowsMu, settings, shouldShowBadges(pollCfg))
		setIdle(time.Now().Format("2006-01-02 15:04:05"))
	}
	_, stopEnabledAtStart, stopAtStart, stoppedAtStart := pollCfg.Snapshot()
	if stopEnabledAtStart && !time.Now().Before(stopAtStart) {
		if !stoppedAtStart {
			pollCfg.MarkStopped()
		}
		applySortAndRender(rows, rowUIs, listVBox, rowsMu, settings, true)
		fyne.Do(func() {
			statusDot.FillColor = colorRed
			statusDot.Refresh()
			updatedText.Text = "Polling stopped"
			updatedText.Refresh()
			countdownText.Color = colorRed
			countdownText.Text = "00:00:00"
			countdownText.Refresh()
		})
	} else {
		runCycle()
	}
	nextPollAt := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cycleMu sync.Mutex
	cycleRunning := false
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			interval, stopEnabled, stopAt, stopped := pollCfg.Snapshot()
			now := time.Now()
			if interval < time.Second {
				interval = time.Second
			}
			if stopped {
				// keep 00:00:00 (or last stopped display)
			} else if stopEnabled {
				remain := time.Until(stopAt)
				if remain <= 0 {
					if !stopped && pollCfg.MarkStopped() {
						applySortAndRender(rows, rowUIs, listVBox, rowsMu, settings, true)
						fyne.Do(func() {
							statusDot.FillColor = colorRed
							statusDot.Refresh()
							updatedText.Text = "Polling stopped"
							updatedText.Refresh()
							countdownText.Color = colorRed
							countdownText.Text = "00:00:00"
							countdownText.Refresh()
							dialog.ShowInformation("已停止", "到达截止时间", win)
						})
					}
					continue
				}
				txt := formatDurationHMS(remain)
				fyne.Do(func() {
					if remain > 30*time.Minute {
						countdownText.Color = colorHeaderText
					} else if remain > 5*time.Minute {
						countdownText.Color = colorGreen
					} else {
						countdownText.Color = colorRed
					}
					countdownText.Text = txt
					countdownText.Refresh()
				})
			} else {
				untilPoll := time.Duration(0)
				if now.Before(nextPollAt) {
					untilPoll = nextPollAt.Sub(now)
				}
				txt := formatDurationHMS(untilPoll)
				fyne.Do(func() {
					countdownText.Color = colorHeaderText
					countdownText.Text = txt
					countdownText.Refresh()
				})
			}
			if stopped {
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
			nextPollAt = now.Add(interval)
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
				if r.hasManual {
					r.LastError = ""
				} else {
				r.LastError = err.Error()
				}
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
			r.hasManual = false
		}(row)
	}
	wg.Wait()
}

// flashToneForRankingTrend mixes directional colour with purple-copper tint (no triangles).
func flashToneForRankingTrend(trend int) color.NRGBA {
	switch {
	case trend > 0:
		return lerpNRGBA(colorFlashRankingUp, colorFlashCopperPurple, 0.28)
	case trend < 0:
		return lerpNRGBA(colorFlashRankingDown, colorFlashCopperPurple, 0.28)
	default:
		return colorFlashCopperPurple
	}
}

// applySortAndRender sorts rows by rating desc, updates each rowUI's text/colors,
// reorders the list VBox, and flashes player names briefly when ratings move (colour = direction).
func applySortAndRender(rows []*playerState, rowUIs []*rowUI, listVBox *fyne.Container, rowsMu *sync.RWMutex, settings *uiSettings, showBadges bool) {
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
		badge   fyne.Resource
		name    string
		bgColor color.NRGBA
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

		ratingStr := "-"
		ratingC := mutedColor
		nameStr := r.Name
		nameC := baseColor
		var badgeRes fyne.Resource = badgeResourceNone
		bgColor := color.NRGBA{}
		if r.LastError == "" {
			ratingStr = strconv.Itoa(r.LiveScore)
			ratingC = ratingColorByScore(r.LiveScore, baseColor)
			if showBadges {
				badgeRes = badgeResourceByRank(pos)
			}
			if pos < 8 {
				bgColor = colorTop8RowGlass
			} else {
				bgColor = colorRowGlass
			}
		} else {
			nameC = mutedColor
			rankStr = "-"
		}

		snapshots[pos] = rowSnapshot{
			ui:      ui,
			rank:    rankStr,
			badge:   badgeRes,
			name:    nameStr,
			bgColor: bgColor,
			nameC:   nameC,
			rating:  ratingStr,
			ratingC: ratingC,
			trend:   r.trend,
		}

		if r.LastError == "" && r.trend != 0 {
			flashes = append(flashes, flashTarget{
				text: ui.nameText,
				from: flashToneForRankingTrend(r.trend),
			})
		}
	}
	rowsMu.RUnlock()

	fyne.Do(func() {
		// Apply text updates
		for _, s := range snapshots {
			s.ui.rankText.Text = s.rank
			s.ui.rankText.Refresh()
			s.ui.background.FillColor = s.bgColor
			s.ui.background.Refresh()

			s.ui.badgeIcon.Resource = s.badge
			s.ui.badgeIcon.Refresh()

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

func badgeResourceByRank(rankIndex int) fyne.Resource {
	switch {
	case rankIndex == 0:
		return badgeResourceChampion
	case rankIndex == 1:
		return badgeResourceRunnerUp
	case rankIndex == 2:
		return badgeResourceThird
	default:
		return badgeResourceNone
	}
}

func ratingColorByScore(score int, fallback color.NRGBA) color.NRGBA {
	switch {
	case score >= 2400:
		return colorRating2400
	case score >= 2300:
		return colorRating2300
	case score >= 2200:
		return colorRating2200
	default:
		return fallback
	}
}

func showManualScoreDialog(win fyne.Window, row *playerState, rowsMu *sync.RWMutex, onSaved func()) {
	entry := widget.NewEntry()
	rowsMu.RLock()
	current := row.LiveScore
	name := row.Name
	rowsMu.RUnlock()
	if current > 0 {
		entry.SetText(strconv.Itoa(current))
	}
	formDlg := dialog.NewForm(
		"Manual Rating",
		"Save",
		"Cancel",
		[]*widget.FormItem{widget.NewFormItem(fmt.Sprintf("%s score", name), entry)},
		func(ok bool) {
			if !ok {
				return
			}
			v, err := strconv.Atoi(strings.TrimSpace(entry.Text))
			if err != nil || v < 0 || v > 4000 {
				dialog.ShowError(errors.New("score must be a number between 0 and 4000"), win)
				return
			}
			rowsMu.Lock()
			row.LiveScore = v
			row.LastError = ""
			row.hasManual = true
			row.prevScore = v
			row.hasPrev = true
			row.trend = 0
			rowsMu.Unlock()
			if onSaved != nil {
				onSaved()
			}
		},
		win,
	)
	entry.OnSubmitted = func(_ string) {
		formDlg.Submit()
	}
	formDlg.Show()
	fyne.Do(func() {
		win.Canvas().Focus(entry)
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
	start := time.Now()
	if err := triggerPlayerUpdate(cwalID, cfg); err != nil {
		logAPIFetch("alias=%s stage=trigger_update status=error err=%v", cwalID, err)
		return profileResult{}, fmt.Errorf("trigger update failed: %w", err)
	}
	time.Sleep(updateRequestWait)

	var lastErr error
	for attempt := 1; attempt <= profileRetryMaxAttempt; attempt++ {
		result, err := queryProfile(cwalID, cfg)
		if err == nil {
			logAPIFetch("alias=%s stage=query_profile status=ok rating=%d elapsed_ms=%d attempts=%d", cwalID, result.Rating, time.Since(start).Milliseconds(), attempt)
			return result, nil
		}
		lastErr = err
		logAPIFetch("alias=%s stage=query_profile status=retry attempt=%d err=%v", cwalID, attempt, err)
		if attempt < profileRetryMaxAttempt {
			time.Sleep(profileRetryInterval)
		}
	}
	logAPIFetch("alias=%s stage=query_profile status=failed elapsed_ms=%d err=%v", cwalID, time.Since(start).Milliseconds(), lastErr)
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
		logAPIFetch("alias=%s endpoint=player-update status=network_error err=%v", cwalID, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logAPIFetch("alias=%s endpoint=player-update status=http_%d body=%s", cwalID, resp.StatusCode, truncateText(string(body), 120))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateText(string(body), 120))
	}
	logAPIFetch("alias=%s endpoint=player-update status=ok code=%d", cwalID, resp.StatusCode)
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
		logAPIFetch("alias=%s endpoint=profile_view status=network_error err=%v", cwalID, err)
		return profileResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logAPIFetch("alias=%s endpoint=profile_view status=http_%d body=%s", cwalID, resp.StatusCode, truncateText(string(body), 120))
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	records, err := parseCSVRecordsWithEncodingFallback(raw)
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

func parseCSVRecordsWithEncodingFallback(raw []byte) ([][]string, error) {
	// UTF-8 BOM
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	if records, err := readCSVRecords(raw); err == nil && utf8.Valid(raw) {
		return records, nil
	}

	// UTF-16 LE/BE BOM (Windows editors may save this way)
	if len(raw) >= 2 {
		if raw[0] == 0xFF && raw[1] == 0xFE {
			if records, err := readCSVRecords([]byte(decodeUTF16(raw[2:], true))); err == nil {
				return records, nil
			}
		}
		if raw[0] == 0xFE && raw[1] == 0xFF {
			if records, err := readCSVRecords([]byte(decodeUTF16(raw[2:], false))); err == nil {
				return records, nil
			}
		}
	}

	// GBK fallback for Excel/Windows ANSI save.
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err == nil {
		if records, csvErr := readCSVRecords(decoded); csvErr == nil {
			return records, nil
		}
	}
	return nil, errors.New("failed to parse users.csv with UTF-8/UTF-16/GBK")
}

func readCSVRecords(data []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	return reader.ReadAll()
}

func decodeUTF16(data []byte, littleEndian bool) string {
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	u16 := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			u16 = append(u16, uint16(data[i])|uint16(data[i+1])<<8)
		} else {
			u16 = append(u16, uint16(data[i])<<8|uint16(data[i+1]))
		}
	}
	return string(utf16.Decode(u16))
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
