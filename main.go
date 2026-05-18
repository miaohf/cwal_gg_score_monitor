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
	settingsFilePath       = "settings.json"
	fetchInterval          = 300 * time.Second // 默认轮询间隔（秒）
	requestTimeout         = 12 * time.Second
	updateRequestWait      = 1200 * time.Millisecond
	profileRetryInterval   = 700 * time.Millisecond
	profileRetryMaxAttempt = 4
	updateAPIURL           = "https://v2.api.cwal.gg/player-update"
	apiLogPath             = "api_fetch.log"

	flashDuration             = 1200 * time.Millisecond
	manualHoldDefaultDuration = 300 * time.Second // 手工分数默认保留时长（秒）

	// Column widths: fixed minima + adaptive player column.
	// Wide window: player column expands; narrow window: columns fall back to minima.
	colRankWidth                          = 20  // 排名列宽度（#）
	colPlayerWidthHardMin                 = 46  // 玩家列“硬下限”：窗口拖到很窄时可压到这个值
	colPlayerWidthMin                     = 50  // 玩家列参考最小宽度（会结合实际文字宽度动态计算）
	colPlayerWidthMax                     = 120 // 玩家列最大宽度（限制中间空白，避免名字和分数离太远）
	colRatingRightAlignStartWidth         = 260 // 窗口超过该宽度后，RATING 列贴近右侧
	colBadgeWidth                         = 10  // 徽章列宽度（冠军/亚军/季军图标列）
	colRatingWidth                        = 54  // 分数列宽度（需容纳 RATING 表头和 4 位分数）
	colPlayerTextPad                      = 6   // 玩家列文字右侧留白（避免名字贴近下一列）
	ratingColPadRight             float32 = 0   // RATING 右侧留白（0=尽量贴右）
	colHBoxPad                    float32 = 1   // horizontal gap between fixed columns
	rowHeight                             = 26
	headerHeight                          = 18
	listRowSpacing                float32 = 1 // gap between leaderboard rows (0 = no vertical gutter)
	footerUpdatedTimeMinSize              = 8 // 底部状态时间最小字号
	footerUpdatedTimeBelowFooter          = 2 // 相对 footer 统计行再小一档

	prefFontSizeKey             = "ui.font_size"
	prefFontColorRKey           = "ui.font_color_r"
	prefFontColorGKey           = "ui.font_color_g"
	prefFontColorBKey           = "ui.font_color_b"
	prefFontColorAKey           = "ui.font_color_a"
	prefFontTypeKey             = "ui.font_type"
	prefWindowOpacityKey        = "ui.window_opacity"
	prefSettingsSavedKey        = "ui.settings_saved"
	prefPollSettingsSaved       = "poll.settings_saved"
	prefPollIntervalSecKey      = "poll.interval_sec"
	prefManualHoldSecKey        = "poll.manual_hold_sec"
	prefPollStopEnabledKey      = "poll.stop_enabled"
	prefPollStopAtKey           = "poll.stop_at"
	prefHistoryScoresKey        = "history.scores_json"
	prefWindowWidthKey          = "window.width"
	prefWindowHeightKey         = "window.height"
	prefWindowsOverlayXKey      = "windows_overlay.x"
	prefWindowsOverlayYKey      = "windows_overlay.y"
	prefWindowsOverlayWidthKey  = "windows_overlay.width"
	prefWindowsOverlayHeightKey = "windows_overlay.height"
	defaultWindowOpacity        = 255
	defaultFontSize             = 16
	defaultFontType             = "Regular"
	appVersion                  = "v2.0"
)

var (
	colorRed   = color.NRGBA{R: 220, G: 38, B: 38, A: 255}
	colorGreen = color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	// Ranking trend flashes (replacing arrows): red — score/mark up; green — score down.
	// «紫铜» mixed into the flash starter for a warm metallic hint.
	colorFlashCopperPurple = color.NRGBA{R: 176, G: 124, B: 148, A: 255}
	colorFlashRankingUp    = color.NRGBA{R: 235, G: 72, B: 90, A: 255}  // rise — red (排名上升感)
	colorFlashRankingDown  = color.NRGBA{R: 34, G: 197, B: 130, A: 255} // fall — green
	colorMuted             = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	colorText              = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	colorHeaderText        = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	// Row panels: translucent milky overlays (real frosted blur is not supported by Fyne).
	colorRowGlass     = color.NRGBA{R: 241, G: 245, B: 249, A: 40}  // default row frost
	colorTop8RowGlass = color.NRGBA{R: 226, G: 232, B: 240, A: 72}  // slightly brighter strip for top 8
	colorRating2200   = color.NRGBA{R: 80, G: 190, B: 255, A: 255}  // cyan-blue
	colorRating2300   = color.NRGBA{R: 170, G: 155, B: 255, A: 255} // bright violet-blue
	colorRating2400   = color.NRGBA{R: 255, G: 210, B: 90, A: 255}  // gold

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
	LiveScore    int
	LastError    string
	hasManual    bool
	manualUntil  time.Time
	LastUpdateOK bool

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
	statusDot  *canvas.Circle
	statusBox  fyne.CanvasObject
	nameText   *canvas.Text
	ratingText *canvas.Text
	container  *fyne.Container
}

type pollControl struct {
	mu          sync.RWMutex
	interval    time.Duration
	manualHold  time.Duration
	stopEnabled bool
	stopAt      time.Time
	stopped     bool
	resetCh     chan struct{} // signals poll loop to reschedule nextPollAt immediately
	kickCh      chan struct{} // signals poll loop to run one cycle ASAP
}

type savedScore struct {
	CwalID string `json:"cwal_id"`
	Score  int    `json:"score"`
}

type savedSettingsFile struct {
	FontSize        float32 `json:"font_size"`
	FontColorR      uint8   `json:"font_color_r"`
	FontColorG      uint8   `json:"font_color_g"`
	FontColorB      uint8   `json:"font_color_b"`
	FontColorA      uint8   `json:"font_color_a"`
	FontType        string  `json:"font_type"`
	WindowOpacity   uint8   `json:"window_opacity"`
	PollIntervalSec int     `json:"poll_interval_sec"`
	ManualHoldSec   int     `json:"manual_hold_sec"`
	StopEnabled     bool    `json:"stop_enabled"`
	StopAt          string  `json:"stop_at"`
	WindowWidth     float32 `json:"window_width"`
	WindowHeight    float32 `json:"window_height"`
	WindowsOverlayX int     `json:"windows_overlay_x"`
	WindowsOverlayY int     `json:"windows_overlay_y"`
	WindowsOverlayW int     `json:"windows_overlay_width"`
	WindowsOverlayH int     `json:"windows_overlay_height"`
}

func newPollControl() *pollControl {
	return &pollControl{
		interval:   fetchInterval,
		manualHold: manualHoldDefaultDuration,
		resetCh:    make(chan struct{}, 1),
		kickCh:     make(chan struct{}, 1),
	}
}

// Reset signals the poll loop to reschedule nextPollAt to now+newInterval.
func (p *pollControl) Reset() {
	select {
	case p.resetCh <- struct{}{}:
	default:
	}
}

// Kick requests one immediate polling cycle (best effort).
func (p *pollControl) Kick() {
	select {
	case p.kickCh <- struct{}{}:
	default:
	}
}

// KickAfter schedules one best-effort immediate cycle after d.
func (p *pollControl) KickAfter(d time.Duration) {
	if d <= 0 {
		p.Kick()
		return
	}
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		<-timer.C
		p.Kick()
	}()
}

func (p *pollControl) Snapshot() (time.Duration, bool, time.Time, bool, time.Duration) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.interval, p.stopEnabled, p.stopAt, p.stopped, p.manualHold
}

func (p *pollControl) Update(interval time.Duration, stopEnabled bool, stopAt time.Time, manualHold time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval = interval
	p.manualHold = manualHold
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
	_, stopEnabled, stopAt, stopped, _ := p.Snapshot()
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
	if intervalSec < 60 || intervalSec > 86400 {
		intervalSec = int(fetchInterval / time.Second)
	}
	manualHoldSec := p.Int(prefManualHoldSecKey)
	if manualHoldSec < 0 || manualHoldSec > 86400 {
		manualHoldSec = int(manualHoldDefaultDuration / time.Second)
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
	pc.Update(
		time.Duration(intervalSec)*time.Second,
		stopEnabled,
		stopAt,
		time.Duration(manualHoldSec)*time.Second,
	)
	return pc
}

func savePollSettingsToPrefs(p fyne.Preferences, interval time.Duration, stopEnabled bool, stopAt time.Time, manualHold time.Duration) {
	p.SetBool(prefPollSettingsSaved, true)
	p.SetInt(prefPollIntervalSecKey, int(interval/time.Second))
	p.SetInt(prefManualHoldSecKey, int(manualHold/time.Second))
	p.SetBool(prefPollStopEnabledKey, stopEnabled)
	if stopEnabled && !stopAt.IsZero() {
		p.SetString(prefPollStopAtKey, stopAt.Format(time.RFC3339))
	} else {
		p.SetString(prefPollStopAtKey, "")
	}
}

func defaultStopTime(now time.Time) time.Time {
	stop := time.Date(now.Year(), now.Month(), now.Day(), 23, 45, 0, 0, now.Location())
	if !stop.After(now) {
		stop = stop.Add(24 * time.Hour)
	}
	return stop
}

func loadWindowSizeFromPrefs(p fyne.Preferences) fyne.Size {
	width := float32(p.Float(prefWindowWidthKey))
	height := float32(p.Float(prefWindowHeightKey))
	if width < 160 || width > 4000 {
		width = 420
	}
	if height < 220 || height > 4000 {
		height = 700
	}
	return fyne.NewSize(width, height)
}

func saveWindowSizeToPrefs(p fyne.Preferences, size fyne.Size) {
	if size.Width < 160 || size.Height < 220 {
		return
	}
	p.SetFloat(prefWindowWidthKey, float64(size.Width))
	p.SetFloat(prefWindowHeightKey, float64(size.Height))
}

func loadSettingsFileIntoPrefs(p fyne.Preferences) bool {
	raw, err := os.ReadFile(settingsFilePath)
	if err != nil {
		return false
	}
	var saved savedSettingsFile
	if err := json.Unmarshal(raw, &saved); err != nil {
		return false
	}
	if saved.FontSize >= 10 && saved.FontSize <= 36 {
		p.SetBool(prefSettingsSavedKey, true)
		p.SetFloat(prefFontSizeKey, float64(saved.FontSize))
		p.SetString(prefFontTypeKey, saved.FontType)
		p.SetInt(prefFontColorRKey, int(saved.FontColorR))
		p.SetInt(prefFontColorGKey, int(saved.FontColorG))
		p.SetInt(prefFontColorBKey, int(saved.FontColorB))
		p.SetInt(prefFontColorAKey, int(saved.FontColorA))
		p.SetInt(prefWindowOpacityKey, int(saved.WindowOpacity))
	}
	if saved.PollIntervalSec > 0 || saved.ManualHoldSec > 0 || saved.StopAt != "" {
		p.SetBool(prefPollSettingsSaved, true)
		p.SetInt(prefPollIntervalSecKey, saved.PollIntervalSec)
		p.SetInt(prefManualHoldSecKey, saved.ManualHoldSec)
		p.SetBool(prefPollStopEnabledKey, saved.StopEnabled)
		p.SetString(prefPollStopAtKey, saved.StopAt)
	}
	if saved.WindowWidth > 0 && saved.WindowHeight > 0 {
		p.SetFloat(prefWindowWidthKey, float64(saved.WindowWidth))
		p.SetFloat(prefWindowHeightKey, float64(saved.WindowHeight))
	}
	if saved.WindowsOverlayW > 0 && saved.WindowsOverlayH > 0 {
		p.SetInt(prefWindowsOverlayXKey, saved.WindowsOverlayX)
		p.SetInt(prefWindowsOverlayYKey, saved.WindowsOverlayY)
		p.SetInt(prefWindowsOverlayWidthKey, saved.WindowsOverlayW)
		p.SetInt(prefWindowsOverlayHeightKey, saved.WindowsOverlayH)
	}
	return true
}

func saveSettingsFileFromPrefs(p fyne.Preferences) {
	ui := loadUISettingsFromPrefs(p).Snapshot()
	poll := loadPollSettingsFromPrefs(p)
	interval, stopEnabled, stopAt, _, manualHold := poll.Snapshot()
	saved := savedSettingsFile{
		FontSize:        ui.FontSize,
		FontColorR:      ui.FontColor.R,
		FontColorG:      ui.FontColor.G,
		FontColorB:      ui.FontColor.B,
		FontColorA:      ui.FontColor.A,
		FontType:        ui.FontType,
		WindowOpacity:   ui.BackgroundAlpha,
		PollIntervalSec: int(interval / time.Second),
		ManualHoldSec:   int(manualHold / time.Second),
		StopEnabled:     stopEnabled,
		WindowWidth:     float32(p.Float(prefWindowWidthKey)),
		WindowHeight:    float32(p.Float(prefWindowHeightKey)),
		WindowsOverlayX: p.Int(prefWindowsOverlayXKey),
		WindowsOverlayY: p.Int(prefWindowsOverlayYKey),
		WindowsOverlayW: p.Int(prefWindowsOverlayWidthKey),
		WindowsOverlayH: p.Int(prefWindowsOverlayHeightKey),
	}
	if stopEnabled && !stopAt.IsZero() {
		saved.StopAt = stopAt.Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(settingsFilePath, raw, 0o644)
}

func nextScoreRefreshAt(now time.Time) time.Time {
	thisMinute := now.Truncate(time.Minute)
	if now.Second() == 0 && now.Nanosecond() == 0 {
		return thisMinute
	}
	return thisMinute.Add(time.Minute)
}

func nextScoreRefreshAfter(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute)
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

type leaderboardColumnLayout struct {
	playerHardMin float32
	playerMin     float32
	playerMax     float32
}

func newLeaderboardColumnLayout(playerHardMin, playerMin, playerMax float32) fyne.Layout {
	return &leaderboardColumnLayout{
		playerHardMin: playerHardMin,
		playerMin:     playerMin,
		playerMax:     playerMax,
	}
}

func computePlayerColumnWidth(rows []*playerState) float32 {
	maxTextWidth := canvas.NewText("PLAYER", colorHeaderText).MinSize().Width
	for _, r := range rows {
		txt := canvas.NewText(strings.TrimSpace(r.Name), colorText)
		txt.TextSize = defaultFontSize
		w := txt.MinSize().Width
		if w > maxTextWidth {
			maxTextWidth = w
		}
	}

	preferred := maxTextWidth + colPlayerTextPad
	if preferred < colPlayerWidthMin {
		preferred = colPlayerWidthMin
	}
	if preferred < colPlayerWidthHardMin {
		preferred = colPlayerWidthHardMin
	}
	if preferred > colPlayerWidthMax {
		preferred = colPlayerWidthMax
	}
	return preferred
}

func (l *leaderboardColumnLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 4 {
		return
	}
	rankW := float32(colRankWidth)
	badgeW := float32(colBadgeWidth)
	ratingW := float32(colRatingWidth)
	gap := colHBoxPad

	fixedW := rankW + badgeW + ratingW
	minTotalWithGap := fixedW + l.playerMin + gap*3
	playerW := l.playerMin
	if size.Width > minTotalWithGap {
		if size.Width >= colRatingRightAlignStartWidth {
			// Wide mode: let PLAYER absorb the remaining width so RATING stays right aligned.
			playerW = size.Width - fixedW - gap*3
		} else {
			// Compact mode: only use part of the extra room to keep names close to ratings.
			playerW += (size.Width - minTotalWithGap) * 0.35
		}
	}
	if playerW < l.playerMin {
		playerW = l.playerMin
	}
	if playerW < l.playerHardMin {
		playerW = l.playerHardMin
	}
	if size.Width < colRatingRightAlignStartWidth && playerW > l.playerMax {
		playerW = l.playerMax
	}

	widths := []float32{rankW, playerW, badgeW, ratingW}
	x := float32(0)
	for i := 0; i < 4; i++ {
		obj := objects[i]
		if obj == nil || !obj.Visible() {
			x += widths[i]
			if i < 3 {
				x += gap
			}
			continue
		}
		obj.Move(fyne.NewPos(x, 0))
		obj.Resize(fyne.NewSize(widths[i], size.Height))
		x += widths[i]
		if i < 3 {
			x += gap
		}
	}
}

func (l *leaderboardColumnLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	height := float32(0)
	for _, obj := range objects {
		if obj == nil || !obj.Visible() {
			continue
		}
		height = fyne.Max(height, obj.MinSize().Height)
	}
	// Enforce a practical minimum width: keep normal gaps and player min width to avoid overlap/truncation.
	width := float32(colRankWidth) + l.playerMin + float32(colBadgeWidth) + float32(colRatingWidth) + colHBoxPad*3
	return fyne.NewSize(width, height)
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
	if runWindowsTransparentMode(rows, cfg) {
		return
	}

	myApp := app.NewWithID("cwalgg.score.monitor")
	if !loadSettingsFileIntoPrefs(myApp.Preferences()) {
		saveUISettingsToPrefs(myApp.Preferences(), defaultUISettings().Snapshot())
	}
	win := myApp.NewWindow("Score Monitor")
	win.Resize(loadWindowSizeFromPrefs(myApp.Preferences()))
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
	updatedText.TextSize = float32(footerUpdatedTimeMinSize)
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
	playerColWidth := computePlayerColumnWidth(rows)
	colHeader, headerRefs := buildHeaderRow(playerColWidth)

	// ---- Rows ----
	rowUIs := make([]*rowUI, len(rows))
	var listVBox *fyne.Container
	var rowsMu sync.RWMutex
	for i := range rows {
		idx := i
		rowUIs[i] = buildRowUI(playerColWidth, func() {
			_, _, _, _, manualHold := pollCfg.Snapshot()
			showManualScoreDialog(win, rows[idx], manualHold, &rowsMu, func() {
				saveHistoryScoresToPrefs(myApp.Preferences(), rows, &rowsMu)
				applySortAndRender(rows, rowUIs, listVBox, &rowsMu, settings, shouldShowBadges(pollCfg))
				// Manual hold is per-row; kick one cycle so other rows can refresh immediately.
				pollCfg.Kick()
				// And kick once after hold expires so the edited row refreshes promptly.
				pollCfg.KickAfter(manualHold)
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
		saveWindowSizeToPrefs(myApp.Preferences(), win.Canvas().Size())
		saveSettingsFileFromPrefs(myApp.Preferences())
		close(stopCh)
		win.Close()
	})

	win.ShowAndRun()
}

func buildHeaderRow(playerColWidth float32) (*fyne.Container, headerUI) {
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
	playerBox := container.NewGridWrap(fyne.NewSize(playerColWidth, headerHeight), player)
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, headerHeight), rating)

	badgeHeaderSlot := canvas.NewRectangle(color.Transparent)
	badgeHeaderBox := container.NewGridWrap(fyne.NewSize(colBadgeWidth, headerHeight), badgeHeaderSlot)
	headerRow := container.New(newLeaderboardColumnLayout(colPlayerWidthHardMin, playerColWidth, colPlayerWidthMax),
		rankBox, playerBox, badgeHeaderBox, ratingBox,
	)
	headerPadded := container.New(layout.NewCustomPaddedLayout(0, 0, 0, ratingColPadRight), headerRow)
	return headerPadded, headerUI{
		rank:   rank,
		player: player,
		rating: rating,
	}
}

func buildRowUI(playerColWidth float32, onDoubleTap func()) *rowUI {
	bg := canvas.NewRectangle(color.Transparent)
	rank := canvas.NewText("", colorMuted)
	rank.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rank.TextSize = 13
	rank.Alignment = fyne.TextAlignLeading

	name := canvas.NewText("", colorText)
	name.TextSize = 13
	name.Alignment = fyne.TextAlignLeading

	badge := canvas.NewImageFromResource(badgeResourceNone)
	badge.FillMode = canvas.ImageFillContain
	badge.SetMinSize(fyne.NewSize(16, 16))
	statusDot := canvas.NewCircle(colorGreen)
	statusDot.Hide()
	statusDotSize := float32(7)
	statusDotPadY := (float32(rowHeight) - statusDotSize) / 2
	statusDotBox := container.New(
		layout.NewCustomPaddedLayout(statusDotPadY, statusDotPadY, 0, 0),
		container.NewGridWrap(fyne.NewSize(statusDotSize, statusDotSize), statusDot),
	)
	statusDotBox.Hide()

	rating := canvas.NewText("", colorText)
	rating.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	rating.TextSize = 13
	rating.Alignment = fyne.TextAlignTrailing
	ratingGroup := container.New(layout.NewCustomPaddedHBoxLayout(2), statusDotBox, rating)

	rankBox := container.NewGridWrap(fyne.NewSize(colRankWidth, rowHeight), rank)
	nameBox := container.NewGridWrap(fyne.NewSize(playerColWidth, rowHeight), name)
	badgeBox := container.NewGridWrap(fyne.NewSize(colBadgeWidth, rowHeight), container.NewCenter(badge))
	ratingBox := container.NewGridWrap(fyne.NewSize(colRatingWidth, rowHeight), container.NewBorder(nil, nil, nil, ratingGroup))

	rowInner := container.New(newLeaderboardColumnLayout(colPlayerWidthHardMin, playerColWidth, colPlayerWidthMax), rankBox, nameBox, badgeBox, ratingBox)
	rowContent := container.New(layout.NewCustomPaddedLayout(0, 0, 0, ratingColPadRight), rowInner)
	row := container.NewMax(bg, rowContent, newDoubleTapBox(rowContent, onDoubleTap))

	return &rowUI{
		background: bg,
		rankText:   rank,
		badgeIcon:  badge,
		statusDot:  statusDot,
		statusBox:  statusDotBox,
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
	interval, stopEnabled, stopAt, _, manualHold := pollCfg.Snapshot()
	intervalEntry.SetText(strconv.Itoa(int(interval / time.Second)))
	manualHoldEntry := widget.NewEntry()
	manualHoldEntry.SetText(strconv.Itoa(int(manualHold / time.Second)))

	stopTimeEntry := widget.NewEntry()
	stopTimeEntry.SetPlaceHolder("YYYY-MM-DD HH:MM")
	if stopEnabled {
		stopTimeEntry.SetText(stopAt.Format("2006-01-02 15:04"))
	} else {
		stopTimeEntry.SetText(defaultStopTime(time.Now()).Format("2006-01-02 15:04"))
	}

	items := []*widget.FormItem{
		widget.NewFormItem("Font Size", sizeEntry),
		widget.NewFormItem("Font Color", colorSelect),
		widget.NewFormItem("Font Type", typeSelect),
		widget.NewFormItem("BG %", alphaRow),
		widget.NewFormItem("Poll(s)", intervalEntry),
		widget.NewFormItem("Hold(s)", manualHoldEntry),
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
		if err != nil || intervalSec < 60 || intervalSec > 86400 {
			dialog.ShowError(errors.New("polling interval must be between 60 and 86400 seconds"), win)
			return
		}
		manualHoldSec, err := strconv.Atoi(strings.TrimSpace(manualHoldEntry.Text))
		if err != nil || manualHoldSec < 0 || manualHoldSec > 86400 {
			dialog.ShowError(errors.New("manual hold must be between 0 and 86400 seconds"), win)
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
		nextInterval := time.Duration(intervalSec) * time.Second
		nextManualHold := time.Duration(manualHoldSec) * time.Second
		pollCfg.Update(nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
		pollCfg.Reset()
		savePollSettingsToPrefs(prefs, nextInterval, nextStopEnabled, nextStopAt, nextManualHold)
		saveSettingsFileFromPrefs(prefs)
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
	manualHoldEntry.OnSubmitted = func(_ string) {
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

	bodyStyle := styleByType(s.FontType)
	headerStyle := bodyStyle
	headerStyle.Bold = true

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
	_, stopEnabledAtStart, stopAtStart, stoppedAtStart, _ := pollCfg.Snapshot()
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
		applySortAndRender(rows, rowUIs, listVBox, rowsMu, settings, shouldShowBadges(pollCfg))
	}
	nextPollAt := nextScoreRefreshAt(time.Now())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cycleMu sync.Mutex
	cycleRunning := false
	for {
		select {
		case <-stopCh:
			return
		case <-pollCfg.resetCh:
			nextPollAt = nextScoreRefreshAt(time.Now())
			continue
		case <-pollCfg.kickCh:
			cycleMu.Lock()
			if cycleRunning {
				cycleMu.Unlock()
				continue
			}
			cycleRunning = true
			nextPollAt = nextScoreRefreshAt(time.Now())
			cycleMu.Unlock()
			go func() {
				runCycle()
				cycleMu.Lock()
				cycleRunning = false
				cycleMu.Unlock()
			}()
			continue
		case <-ticker.C:
			_, stopEnabled, stopAt, stopped, _ := pollCfg.Snapshot()
			now := time.Now()
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
			now := time.Now()
			rowsMu.Lock()
			if r.hasManual {
				if r.manualUntil.After(now) {
					// Manual score is still in hold period, skip API overwrite.
					r.LastError = ""
					r.LastUpdateOK = false
					rowsMu.Unlock()
					return
				}
				r.hasManual = false
				r.manualUntil = time.Time{}
			}
			rowsMu.Unlock()

			result, err := fetchPlayerProfile(r.Name, r.CwalID, cfg)

			rowsMu.Lock()
			defer rowsMu.Unlock()
			if err != nil {
				if r.hasManual && r.manualUntil.After(time.Now()) {
					r.LastError = ""
				} else {
					r.LastError = err.Error()
				}
				r.LastUpdateOK = false
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
			r.LastUpdateOK = true
			r.hasManual = false
			r.manualUntil = time.Time{}
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
		ui       *rowUI
		rank     string
		badge    fyne.Resource
		name     string
		bgColor  color.NRGBA
		nameC    color.NRGBA
		rating   string
		ratingC  color.NRGBA
		trend    int
		updateOK bool
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
				bgColor = color.NRGBA{}
			}
		} else {
			nameC = mutedColor
			rankStr = "-"
		}

		snapshots[pos] = rowSnapshot{
			ui:       ui,
			rank:     rankStr,
			badge:    badgeRes,
			name:     nameStr,
			bgColor:  bgColor,
			nameC:    nameC,
			rating:   ratingStr,
			ratingC:  ratingC,
			trend:    r.trend,
			updateOK: !showBadges && r.LastUpdateOK,
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
			if s.badge == badgeResourceNone && s.updateOK {
				s.ui.statusBox.Show()
			} else {
				s.ui.statusBox.Hide()
			}
			s.ui.badgeIcon.Refresh()
			s.ui.statusBox.Refresh()
			s.ui.statusDot.Refresh()

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

func showManualScoreDialog(win fyne.Window, row *playerState, manualHold time.Duration, rowsMu *sync.RWMutex, onSaved func()) {
	entry := widget.NewEntry()
	rowsMu.RLock()
	current := row.LiveScore
	name := row.Name
	rowsMu.RUnlock()
	if current > 0 {
		entry.SetText(strconv.Itoa(current))
		entry.CursorColumn = len([]rune(entry.Text))
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
			row.LastUpdateOK = false
			row.hasManual = manualHold > 0
			if manualHold > 0 {
				row.manualUntil = time.Now().Add(manualHold)
			} else {
				row.manualUntil = time.Time{}
			}
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
		entry.CursorColumn = len([]rune(entry.Text))
		entry.Refresh()
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

// setAPIAuthHeaders sets apikey and Authorization (Bearer + key) for Supabase-style APIs.
func setAPIAuthHeaders(req *http.Request, apiKey string) {
	if apiKey == "" {
		return
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func fetchPlayerProfile(playerName, cwalID string, cfg apiConfig) (profileResult, error) {
	start := time.Now()
	if err := triggerPlayerUpdate(playerName, cwalID, cfg); err != nil {
		logAPIFetch("name=%s cwal_gg_id=%s stage=trigger_update status=error err=%v", playerName, cwalID, err)
		return profileResult{}, fmt.Errorf("trigger update failed: %w", err)
	}
	time.Sleep(updateRequestWait)

	var lastErr error
	for attempt := 1; attempt <= profileRetryMaxAttempt; attempt++ {
		result, err := queryProfile(playerName, cwalID, cfg)
		if err == nil {
			logAPIFetch("name=%s cwal_gg_id=%s stage=query_profile status=ok rating=%d elapsed_ms=%d attempts=%d", playerName, cwalID, result.Rating, time.Since(start).Milliseconds(), attempt)
			return result, nil
		}
		lastErr = err
		if attempt < profileRetryMaxAttempt {
			time.Sleep(profileRetryInterval)
		}
	}
	logAPIFetch(
		"name=%s cwal_gg_id=%s stage=query_profile status=retry attempt=%d elapsed_ms=%d err=%v",
		playerName,
		cwalID,
		profileRetryMaxAttempt,
		time.Since(start).Milliseconds(),
		lastErr,
	)
	return profileResult{}, fmt.Errorf("query profile failed: %w", lastErr)
}

func triggerPlayerUpdate(playerName, cwalID string, cfg apiConfig) error {
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
	setAPIAuthHeaders(req, cfg.APIKey)

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		logAPIFetch("name=%s cwal_gg_id=%s endpoint=player-update status=network_error err=%v", playerName, cwalID, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logAPIFetch("name=%s cwal_gg_id=%s endpoint=player-update status=http_%d body=%s", playerName, cwalID, resp.StatusCode, truncateText(string(body), 120))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateText(string(body), 120))
	}
	logAPIFetch("name=%s cwal_gg_id=%s endpoint=player-update status=ok code=%d", playerName, cwalID, resp.StatusCode)
	return nil
}

func queryProfile(playerName, cwalID string, cfg apiConfig) (profileResult, error) {
	if cfg.ProfileURLTemplate == "" {
		return profileResult{}, errors.New("api_url not configured (set api_url in .env or README)")
	}
	escapedID := url.QueryEscape(cwalID)
	targetURL := strings.Replace(cfg.ProfileURLTemplate, "{cwal_gg_id}", escapedID, 1)

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return profileResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	setAPIAuthHeaders(req, cfg.APIKey)

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		logAPIFetch("name=%s cwal_gg_id=%s endpoint=profile_view status=network_error err=%v", playerName, cwalID, err)
		return profileResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logAPIFetch("name=%s cwal_gg_id=%s endpoint=profile_view status=http_%d body=%s", playerName, cwalID, resp.StatusCode, truncateText(string(body), 120))
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

// loadDotEnv parses a .env file of key=value lines and returns the map.
// Lines starting with '#' and blank lines are ignored.
func loadDotEnv(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		m[k] = v
	}
	return m
}

func loadAPIConfigFromReadme(path string) (apiConfig, error) {
	cfg := apiConfig{}

	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
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
		}
		if err := scanner.Err(); err != nil {
			return apiConfig{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return apiConfig{}, err
	}

	// .env overrides README; api_url / url can supply the profile or matches endpoint template.
	if env := loadDotEnv(".env"); env != nil {
		if v := strings.TrimSpace(env["api_url"]); v != "" {
			cfg.ProfileURLTemplate = v
		} else if v := strings.TrimSpace(env["url"]); v != "" {
			cfg.ProfileURLTemplate = v
		}
		if v, ok := env["api_key"]; ok {
			v = strings.TrimSpace(v)
			if v != "" {
				cfg.APIKey = v
			}
		}
	}

	if cfg.ProfileURLTemplate == "" {
		return apiConfig{}, errors.New("missing API URL: set api_url in .env or add a player_profile_view https URL line in README")
	}
	if cfg.APIKey == "" {
		return apiConfig{}, errors.New("missing API key: set api_key in .env or Apikey in README")
	}
	cfg.Authorization = "Bearer " + cfg.APIKey
	return cfg, nil
}
