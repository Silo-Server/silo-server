package emailalerts

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The email alerts plugin's saved settings import as they are.
func TestPluginSettingsImport(t *testing.T) {
	raw, err := os.ReadFile("testdata/plugin_settings.json")
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	s.Normalize()
	if s.Schedule.Timezone != "Europe/London" || s.Schedule.DailyHour != 7 || s.Schedule.WeeklyDay != 5 {
		t.Fatalf("schedule = %+v", s.Schedule)
	}
	if s.Theme.BackdropDim != 72 || !s.Theme.OpenInApp || s.Delivery.InstantGapMinutes != 10 || s.Delivery.SettleMinutes != 1 {
		t.Fatalf("theme/delivery not kept: %+v %+v", s.Theme, s.Delivery)
	}
	if err := ValidateTemplates(s); err != nil {
		t.Fatalf("templates: %v", err)
	}
}

func TestRenderDefaultTemplates(t *testing.T) {
	s := DefaultSettings()
	env := RenderEnv{Settings: s, ServerName: "Silo", PublicURL: "https://silo.example", Mode: ModeInstant,
		Now: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC), RecipientName: "Alex Example",
		ManageURL: "https://silo.example/manage", UnsubscribeURL: "https://silo.example/unsub",
		OpenURL: "https://silo.example/open"}
	out, err := Render(context.Background(), env, SampleEntries(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Subject, "Silo") {
		t.Fatalf("subject = %q", out.Subject)
	}
	for _, want := range []string{"Harbour Lights", "The Long Way North", "https://silo.example/manage", "https://silo.example/open?id="} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("HTML is missing %q", want)
		}
	}
	if !strings.Contains(out.Text, "Harbour Lights") {
		t.Errorf("text is missing the show")
	}
}

func TestRenderEmbedsFetchedArtwork(t *testing.T) {
	s := DefaultSettings()
	entries := SampleEntries()
	entries[2].Card.PosterURL = "https://silo.example/art/poster.jpg"
	env := RenderEnv{Settings: s, ServerName: "Silo", Mode: ModeInstant, Now: time.Now()}
	fetch := func(context.Context, string) ([]byte, string, error) { return []byte("jpeg"), "image/jpeg", nil }
	out, err := Render(context.Background(), env, entries, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 || !strings.Contains(out.HTML, "cid:"+out.Images[0].CID) {
		t.Fatalf("poster not embedded: %d images", len(out.Images))
	}
}

func TestTemplateErrorIsReported(t *testing.T) {
	s := DefaultSettings()
	s.Templates.Subject = "{{#shows}}unclosed"
	if err := ValidateTemplates(s); err == nil {
		t.Fatal("broken template accepted")
	}
}

func TestDigestDue(t *testing.T) {
	s := DefaultSettings()
	s.Schedule = Schedule{Timezone: "UTC", DailyHour: 7, WeeklyDay: 5, WeeklyHour: 18}
	friday := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC) // a Friday
	cases := []struct {
		leg       string
		last, now time.Time
		want      bool
	}{
		{LegDaily, friday.Add(-16 * time.Hour), friday.Add(6 * time.Hour), false}, // sent Thursday 08:00
		{LegDaily, friday.Add(-20 * time.Hour), friday.Add(6 * time.Hour), true},  // missed Thursday 07:00
		{LegDaily, friday.Add(-20 * time.Hour), friday.Add(7 * time.Hour), true},
		{LegDaily, friday.Add(7 * time.Hour), friday.Add(9 * time.Hour), false},
		{LegWeekly, friday.Add(-7*24*time.Hour + 19*time.Hour), friday.Add(17 * time.Hour), false},
		{LegWeekly, friday.Add(-7*24*time.Hour + 19*time.Hour), friday.Add(18 * time.Hour), true},
		{LegWeekly, friday.Add(18 * time.Hour), friday.Add(5 * 24 * time.Hour), false},
		{LegInstant, friday, friday, true},
	}
	for i, c := range cases {
		if got := s.DigestDue(c.leg, c.last, c.now); got != c.want {
			t.Errorf("case %d (%s): DigestDue = %t, want %t", i, c.leg, got, c.want)
		}
	}
}

func TestInstantHeldUntil(t *testing.T) {
	s := DefaultSettings()
	s.Schedule.Timezone = "UTC"
	s.Delivery.InstantGapMinutes = 10
	s.Delivery.InstantDailyCap = 2
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if held := s.InstantHeldUntil(InstantState{LastInstant: now.Add(-4 * time.Minute)}, now); !held.Equal(now.Add(6 * time.Minute)) {
		t.Fatalf("gap: held until %v", held)
	}
	if held := s.InstantHeldUntil(InstantState{LastInstant: now.Add(-11 * time.Minute)}, now); !held.IsZero() {
		t.Fatalf("past the gap still held until %v", held)
	}
	capped := InstantState{LastInstant: now.Add(-time.Hour), Day: "2026-10-27", DayCount: 2}
	if held := s.InstantHeldUntil(capped, now); !held.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily cap: held until %v", held)
	}
}

func TestMaxAge(t *testing.T) {
	s := DefaultSettings()
	if got := s.MaxAge(0, LegInstant); got != 168*time.Hour {
		t.Fatalf("default = %v", got)
	}
	if got := s.MaxAge(24, LegWeekly); got != 168*time.Hour {
		t.Fatalf("weekly floor = %v", got)
	}
	if got := s.MaxAge(48, LegDaily); got != 48*time.Hour {
		t.Fatalf("own choice = %v", got)
	}
}
