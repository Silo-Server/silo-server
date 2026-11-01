// Package emailalerts renders and schedules the server's new-media emails:
// editable Mustache templates, a theme, and per-recipient instant, daily and
// weekly sends. It holds no database or transport code; the notifications
// package feeds it catalog entries and delivers what it renders.
package emailalerts

import (
	"fmt"
	"strings"
	"time"
)

// Recipient modes. instant_daily sends instant alerts and, at the daily
// digest hour, a digest recapping the whole day.
const (
	ModeInstant      = "instant"
	ModeInstantDaily = "instant_daily"
	// ModeInstantWeekly sends instant alerts and a weekly digest.
	ModeInstantWeekly = "instant_weekly"
	ModeDaily         = "daily"
	ModeWeekly        = "weekly"
)

// Legs are the separate sends a mode makes, each with its own state.
const (
	LegInstant = "instant"
	LegDaily   = "daily"
	LegWeekly  = "weekly"
)

// ValidMode reports whether mode is a recipient mode.
func ValidMode(mode string) bool {
	switch mode {
	case ModeInstant, ModeInstantDaily, ModeInstantWeekly, ModeDaily, ModeWeekly:
		return true
	}
	return false
}

// Legs returns the sends a mode makes.
func Legs(mode string) []string {
	switch mode {
	case ModeInstantDaily:
		return []string{LegInstant, LegDaily}
	case ModeInstantWeekly:
		return []string{LegInstant, LegWeekly}
	case ModeDaily:
		return []string{LegDaily}
	case ModeWeekly:
		return []string{LegWeekly}
	}
	return []string{LegInstant}
}

// Settings is the server-wide email design and delivery configuration. The
// JSON shape matches the email alerts plugin's, so its settings import as is.
type Settings struct {
	Templates   Templates   `json:"templates"`
	Theme       Theme       `json:"theme"`
	CustomVars  []CustomVar `json:"custom_vars"`
	Schedule    Schedule    `json:"schedule"`
	Delivery    Delivery    `json:"delivery"`
	SelfService SelfService `json:"self_service"`
}

type Templates struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
}

type Theme struct {
	AccentColor     string `json:"accent_color"`
	BackgroundColor string `json:"background_color"`
	CardColor       string `json:"card_color"`
	TextColor       string `json:"text_color"`
	MutedColor      string `json:"muted_color"`
	LogoURL         string `json:"logo_url"`
	HeaderTitle     string `json:"header_title"`
	Intro           string `json:"intro"`
	FooterText      string `json:"footer_text"`
	ButtonText      string `json:"button_text"`
	// BackdropBehindShow puts the show's backdrop behind its name and
	// synopsis, darkened by BackdropDim percent with the card colour.
	BackdropBehindShow bool `json:"backdrop_behind_show"`
	BackdropDim        int  `json:"backdrop_dim"`
	// OpenInApp routes item links through a page that opens the Silo app
	// when it's installed on the reader's phone.
	OpenInApp bool `json:"open_in_app"`
}

type CustomVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Schedule struct {
	Timezone   string `json:"timezone"`
	DailyHour  int    `json:"daily_hour"`
	WeeklyDay  int    `json:"weekly_day"` // 0 = Sunday
	WeeklyHour int    `json:"weekly_hour"`
}

type Delivery struct {
	Paused         bool `json:"paused"`
	SettleMinutes  int  `json:"settle_minutes"`
	MaxItems       int  `json:"max_items"`
	EmbedImages    bool `json:"embed_images"`
	MaxImages      int  `json:"max_images"`
	SynopsisLength int  `json:"synopsis_length"`
	// InstantGapMinutes is the least time between two instant emails to one
	// recipient; items that arrive sooner are held and sent together.
	InstantGapMinutes int `json:"instant_gap_minutes"`
	// InstantDailyCap limits instant emails per recipient per day (0 = no
	// limit); items past the cap go out in the first email of the next day.
	InstantDailyCap int `json:"instant_daily_cap"`
	// MaxAgeHours is the default for how recently an item must have been
	// added to be emailed at all. Recipients can choose their own.
	MaxAgeHours int `json:"max_age_hours"`
}

// SelfService controls profile sign-up: whether profiles may email
// themselves, and whether their address must be confirmed first.
type SelfService struct {
	Enabled             bool `json:"enabled"`
	RequireVerification bool `json:"require_verification"`
}

// DefaultSettings is the starting configuration.
func DefaultSettings() Settings {
	return Settings{
		Templates: Templates{
			Subject: defaultSubjectTemplate,
			HTML:    defaultHTMLTemplate,
			Text:    defaultTextTemplate,
		},
		Theme: Theme{
			AccentColor:        "#7c5cff",
			BackgroundColor:    "#0f1115",
			CardColor:          "#181b22",
			TextColor:          "#f2f3f5",
			MutedColor:         "#9aa0ab",
			HeaderTitle:        "New on {{server_name}}",
			Intro:              "Here's what just landed on the server.",
			FooterText:         "You're receiving this because you signed up for new media alerts.",
			ButtonText:         "Watch now",
			BackdropBehindShow: true,
			BackdropDim:        72,
			OpenInApp:          true,
		},
		Schedule: Schedule{DailyHour: 18, WeeklyDay: 5, WeeklyHour: 18},
		Delivery: Delivery{
			SettleMinutes:     5,
			MaxItems:          60,
			EmbedImages:       true,
			MaxImages:         40,
			SynopsisLength:    320,
			InstantGapMinutes: 10,
			MaxAgeHours:       168,
		},
		SelfService: SelfService{Enabled: true, RequireVerification: true},
	}
}

// Normalize fills empty templates with the defaults and clamps numbers into
// range.
func (s *Settings) Normalize() {
	def := DefaultSettings()
	if strings.TrimSpace(s.Templates.Subject) == "" {
		s.Templates.Subject = def.Templates.Subject
	}
	if strings.TrimSpace(s.Templates.HTML) == "" {
		s.Templates.HTML = def.Templates.HTML
	}
	if strings.TrimSpace(s.Templates.Text) == "" {
		s.Templates.Text = def.Templates.Text
	}
	s.Delivery.SettleMinutes = clamp(s.Delivery.SettleMinutes, 0, 240)
	if s.Delivery.MaxItems <= 0 {
		s.Delivery.MaxItems = def.Delivery.MaxItems
	}
	s.Delivery.MaxItems = clamp(s.Delivery.MaxItems, 1, 500)
	s.Delivery.MaxImages = clamp(s.Delivery.MaxImages, 0, 150)
	if s.Delivery.SynopsisLength < 0 {
		s.Delivery.SynopsisLength = 0
	}
	s.Theme.BackdropDim = clamp(s.Theme.BackdropDim, 0, 95)
	s.Delivery.InstantGapMinutes = clamp(s.Delivery.InstantGapMinutes, 0, 1440)
	s.Delivery.InstantDailyCap = clamp(s.Delivery.InstantDailyCap, 0, 500)
	if s.Delivery.MaxAgeHours <= 0 {
		s.Delivery.MaxAgeHours = def.Delivery.MaxAgeHours
	}
	s.Delivery.MaxAgeHours = clamp(s.Delivery.MaxAgeHours, 1, MaxAgeLimit)
	s.Schedule.DailyHour = clamp(s.Schedule.DailyHour, 0, 23)
	s.Schedule.WeeklyHour = clamp(s.Schedule.WeeklyHour, 0, 23)
	s.Schedule.WeeklyDay = clamp(s.Schedule.WeeklyDay, 0, 6)
	if s.Schedule.Timezone != "" {
		if _, err := time.LoadLocation(s.Schedule.Timezone); err != nil {
			s.Schedule.Timezone = ""
		}
	}
	cleaned := s.CustomVars[:0]
	for _, v := range s.CustomVars {
		v.Key = strings.TrimSpace(v.Key)
		if v.Key != "" {
			cleaned = append(cleaned, v)
		}
	}
	s.CustomVars = cleaned
}

// Location is the schedule's time zone, or the server's.
func (s Settings) Location() *time.Location {
	if s.Schedule.Timezone != "" {
		if loc, err := time.LoadLocation(s.Schedule.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// ValidateTemplates renders the templates against sample content and
// returns the first template error.
func ValidateTemplates(s Settings) error {
	sample := buildData(RenderEnv{Settings: s, ServerName: "Silo", Mode: "preview", Now: time.Now()}, SampleEntries(), newImageSet(true, ""), true)
	_, err := renderTemplates(s, sample)
	return err
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// MaxAgeChoices are the "count as new if added within" options offered to
// recipients, in hours; 0 means the server default.
var MaxAgeChoices = []int{24, 48, 72, 168, 336, 720}

// MaxAgeLimit is the longest max age, in hours.
const MaxAgeLimit = 720

// CleanMaxAge returns h when it is one of the choices, else 0 (the default).
func CleanMaxAge(h int) int {
	for _, c := range MaxAgeChoices {
		if h == c {
			return h
		}
	}
	return 0
}

// MaxAgeLabel describes a max age in hours, such as "48 hours" or "1 week".
func MaxAgeLabel(h int) string {
	switch {
	case h%168 == 0:
		return plural(h/168, "week", "weeks")
	case h%24 == 0 && h > 48:
		return plural(h/24, "day", "days")
	}
	return plural(h, "hour", "hours")
}

// MaxAgeOption is one choice for pages.
type MaxAgeOption struct {
	Hours int    `json:"hours"`
	Label string `json:"label"`
}

// MaxAgeOptions lists the choices for pages, with the server default first.
func MaxAgeOptions(s Settings) []MaxAgeOption {
	out := []MaxAgeOption{{0, "Server default (" + MaxAgeLabel(s.Delivery.MaxAgeHours) + ")"}}
	for _, h := range MaxAgeChoices {
		out = append(out, MaxAgeOption{h, MaxAgeLabel(h)})
	}
	return out
}

// MaxAge is how recently an item must have been added to count as new for a
// recipient whose own choice is hours (0 = the server default). A weekly
// digest always reaches back at least a week.
func (s Settings) MaxAge(hours int, leg string) time.Duration {
	if hours <= 0 {
		hours = s.Delivery.MaxAgeHours
	}
	if leg == LegWeekly && hours < 168 {
		hours = 168
	}
	return time.Duration(hours) * time.Hour
}

// CleanTypes keeps the recognised item types.
func CleanTypes(types []string) []string {
	var out []string
	for _, t := range types {
		if t == "movie" || t == "episode" {
			out = append(out, t)
		}
	}
	return out
}

// InstantState is the rate-limit state of one recipient's instant leg.
type InstantState struct {
	LastInstant time.Time
	Day         string
	DayCount    int
}

// InstantHeldUntil returns when an instant recipient may next be emailed, or
// zero when they may be emailed now.
func (s Settings) InstantHeldUntil(st InstantState, now time.Time) time.Time {
	var until time.Time
	if gap := time.Duration(s.Delivery.InstantGapMinutes) * time.Minute; gap > 0 && !st.LastInstant.IsZero() {
		if next := st.LastInstant.Add(gap); now.Before(next) {
			until = next
		}
	}
	if limit := s.Delivery.InstantDailyCap; limit > 0 {
		local := now.In(s.Location())
		if st.Day == local.Format("2006-01-02") && st.DayCount >= limit {
			midnight := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location())
			if midnight.After(until) {
				until = midnight
			}
		}
	}
	return until
}

// Day is the schedule-local calendar day of t, for the daily cap.
func (s Settings) Day(t time.Time) string {
	return t.In(s.Location()).Format("2006-01-02")
}

// DigestDue reports whether a digest leg last run at lastRun is due at now.
func (s Settings) DigestDue(leg string, lastRun, now time.Time) bool {
	switch leg {
	case LegDaily:
		return lastRun.Before(lastBoundary(now, s.Location(), -1, s.Schedule.DailyHour))
	case LegWeekly:
		return lastRun.Before(lastBoundary(now, s.Location(), s.Schedule.WeeklyDay, s.Schedule.WeeklyHour))
	}
	return true
}

// NextDue is when a mode's next digest goes out, or zero for instant only.
func (s Settings) NextDue(mode string, now time.Time) time.Time {
	switch mode {
	case ModeDaily, ModeInstantDaily:
		return nextBoundary(now, s.Location(), -1, s.Schedule.DailyHour)
	case ModeWeekly, ModeInstantWeekly:
		return nextBoundary(now, s.Location(), s.Schedule.WeeklyDay, s.Schedule.WeeklyHour)
	}
	return time.Time{}
}

// lastBoundary returns the most recent time at or before now that falls on
// hour (and on weekday, unless weekday is -1).
func lastBoundary(now time.Time, loc *time.Location, weekday, hour int) time.Time {
	local := now.In(loc)
	b := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc)
	for range 8 {
		if !b.After(local) && (weekday < 0 || int(b.Weekday()) == weekday) {
			return b
		}
		b = b.AddDate(0, 0, -1)
	}
	return b
}

func nextBoundary(now time.Time, loc *time.Location, weekday, hour int) time.Time {
	local := now.In(loc)
	b := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc)
	for range 8 {
		if b.After(local) && (weekday < 0 || int(b.Weekday()) == weekday) {
			return b
		}
		b = b.AddDate(0, 0, 1)
	}
	return b
}

// RenderMode is the template mode for a leg ("instant", "daily", "weekly").
func RenderMode(leg string) string {
	if leg == LegDaily || leg == LegWeekly {
		return leg
	}
	return ModeInstant
}

// ModeLabel describes a recipient mode for pages and the send log.
func ModeLabel(mode string) string {
	switch mode {
	case ModeInstantDaily:
		return "Instant + daily digest"
	case ModeInstantWeekly:
		return "Instant + weekly digest"
	case ModeDaily:
		return "Daily digest"
	case ModeWeekly:
		return "Weekly digest"
	case ModeInstant:
		return "Instant"
	}
	return fmt.Sprintf("Unknown (%s)", mode)
}
