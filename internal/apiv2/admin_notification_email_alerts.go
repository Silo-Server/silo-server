package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/emailalerts"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// AdminEmailAlertsService manages the new-media emails: design, templates,
// schedule, recipients and sends.
type AdminEmailAlertsService interface {
	State(context.Context) (*notifications.EmailAlertsState, error)
	SaveSettings(context.Context, emailalerts.Settings) (emailalerts.Settings, error)
	Preview(context.Context, emailalerts.Settings, string, string) (*emailalerts.Rendered, string, error)
	SendTest(context.Context, *emailalerts.Settings, string, string, string) (string, int, error)
	RunDue(context.Context, string) *notifications.EmailAlertsReport
	SaveAdminRecipient(context.Context, notifications.EmailAlertRecipient) (notifications.EmailAlertRecipient, error)
	UpdateRecipient(context.Context, string, notifications.EmailAlertRecipient) (notifications.EmailAlertRecipient, error)
	DeleteRecipient(context.Context, string) error
	Rewind(context.Context, string, int) error
}

// The wire types below mirror the emailalerts and notifications types field
// for field, with the same JSON names; convertJSON maps between them.

type EmailAlertTemplates struct {
	Subject string `json:"subject" doc:"Mustache template for the subject line."`
	HTML    string `json:"html" doc:"Mustache template for the HTML body."`
	Text    string `json:"text" doc:"Mustache template for the plain-text body."`
}

type EmailAlertTheme struct {
	AccentColor        string `json:"accent_color"`
	BackgroundColor    string `json:"background_color"`
	CardColor          string `json:"card_color"`
	TextColor          string `json:"text_color"`
	MutedColor         string `json:"muted_color"`
	LogoURL            string `json:"logo_url" doc:"Logo image URL as entered; may be empty, so not validated as a URI"`
	HeaderTitle        string `json:"header_title"`
	Intro              string `json:"intro"`
	FooterText         string `json:"footer_text"`
	ButtonText         string `json:"button_text"`
	BackdropBehindShow bool   `json:"backdrop_behind_show"`
	BackdropDim        int    `json:"backdrop_dim" minimum:"0" maximum:"95"`
	OpenInApp          bool   `json:"open_in_app" doc:"Item links open the Silo app when it is installed."`
}

type EmailAlertCustomVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type EmailAlertSchedule struct {
	Timezone   string `json:"timezone" doc:"IANA time zone for digests; empty uses the server's."`
	DailyHour  int    `json:"daily_hour" minimum:"0" maximum:"23"`
	WeeklyDay  int    `json:"weekly_day" minimum:"0" maximum:"6" doc:"0 is Sunday."`
	WeeklyHour int    `json:"weekly_hour" minimum:"0" maximum:"23"`
}

type EmailAlertDelivery struct {
	Paused            bool `json:"paused"`
	SettleMinutes     int  `json:"settle_minutes" doc:"How long after an item arrives before it is emailed."`
	MaxItems          int  `json:"max_items"`
	EmbedImages       bool `json:"embed_images"`
	MaxImages         int  `json:"max_images"`
	SynopsisLength    int  `json:"synopsis_length"`
	InstantGapMinutes int  `json:"instant_gap_minutes" doc:"Least time between two instant emails to one recipient."`
	InstantDailyCap   int  `json:"instant_daily_cap" doc:"Most instant emails per recipient per day; 0 is no limit."`
	MaxAgeHours       int  `json:"max_age_hours" doc:"Default for how recently an item must have been added to count as new."`
}

type EmailAlertSelfService struct {
	Enabled             bool `json:"enabled" doc:"Profiles may email themselves."`
	RequireVerification bool `json:"require_verification"`
}

type EmailAlertSettings struct {
	Templates   EmailAlertTemplates   `json:"templates"`
	Theme       EmailAlertTheme       `json:"theme"`
	CustomVars  []EmailAlertCustomVar `json:"custom_vars"`
	Schedule    EmailAlertSchedule    `json:"schedule"`
	Delivery    EmailAlertDelivery    `json:"delivery"`
	SelfService EmailAlertSelfService `json:"self_service"`
}

type EmailAlertRecipient struct {
	Kind        string    `json:"kind" enum:"admin,profile"`
	ID          string    `json:"id,omitempty"`
	UserID      int       `json:"user_id,omitempty"`
	ProfileID   string    `json:"profile_id,omitempty"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Enabled     bool      `json:"enabled"`
	Mode        string    `json:"mode" enum:"instant,instant_daily,instant_weekly,daily,weekly"`
	Content     string    `json:"content" enum:"followed,all"`
	Types       []string  `json:"types"`
	LibraryIDs  []int     `json:"library_ids"`
	MaxAgeHours int       `json:"max_age_hours"`
	CreatedAt   time.Time `json:"created_at"`
}

type EmailAlertRecipientStatus struct {
	EmailAlertRecipient
	Key         string     `json:"key" doc:"r:<id> for an added address, p:<user id>:<profile id> for a profile."`
	Active      bool       `json:"active"`
	LastSent    *time.Time `json:"last_sent,omitempty"`
	Failures    int        `json:"failures,omitempty"`
	NextAttempt *time.Time `json:"next_attempt,omitempty"`
	HeldUntil   *time.Time `json:"held_until,omitempty"`
	NextDue     *time.Time `json:"next_due,omitempty"`
}

type EmailAlertLibrary struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
}

type EmailAlertMaxAgeOption struct {
	Hours int    `json:"hours"`
	Label string `json:"label"`
}

type EmailAlertRunReport struct {
	Time       time.Time `json:"time"`
	Reason     string    `json:"reason"`
	Recipients int       `json:"recipients"`
	Sent       int       `json:"sent"`
	Items      int       `json:"items" doc:"Items the sent emails covered."`
	Skipped    string    `json:"skipped,omitempty"`
	Errors     []string  `json:"errors,omitempty"`
}

type EmailAlertLogEntry struct {
	SentAt       time.Time `json:"sent_at"`
	RecipientKey string    `json:"recipient_key"`
	Email        string    `json:"email"`
	Leg          string    `json:"leg"`
	Items        int       `json:"items"`
	Subject      string    `json:"subject"`
	Error        string    `json:"error,omitempty"`
}

type EmailAlertsOverview struct {
	Settings      EmailAlertSettings          `json:"settings"`
	Defaults      EmailAlertSettings          `json:"defaults"`
	Recipients    []EmailAlertRecipientStatus `json:"recipients"`
	Libraries     []EmailAlertLibrary         `json:"libraries"`
	MaxAgeOptions []EmailAlertMaxAgeOption    `json:"max_age_options"`
	Problems      []string                    `json:"problems"`
	Report        *EmailAlertRunReport        `json:"report"`
	Log           []EmailAlertLogEntry        `json:"log"`
	PublicURL     string                      `json:"public_url" doc:"Server public URL emails link to; empty when unset, so not validated as a URI"`
	ServerName    string                      `json:"server_name"`
	SMTPHost      string                      `json:"smtp_host"`
	FromAddress   string                      `json:"from_address"`
	Now           time.Time                   `json:"now"`
}

// convertJSON maps between types that share their JSON shape.
func convertJSON[T any](v any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

type AdminEmailAlertsStateOutput struct{ Body EmailAlertsOverview }

type AdminEmailAlertsSettingsInput struct {
	Body struct {
		Settings EmailAlertSettings `json:"settings"`
	}
}

type AdminEmailAlertsPreviewInput struct {
	Body struct {
		Settings EmailAlertSettings `json:"settings"`
		Mode     string             `json:"mode,omitempty" enum:"instant,daily,weekly"`
		Source   string             `json:"source,omitempty" enum:"latest,sample" doc:"latest renders the newest items; sample renders placeholder content."`
	}
}

type AdminEmailAlertsPreview struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
	Note    string `json:"note,omitempty"`
	Error   string `json:"error,omitempty" doc:"Template error; the other fields are empty."`
}

type AdminEmailAlertsPreviewOutput struct{ Body AdminEmailAlertsPreview }

type AdminEmailAlertsTestInput struct {
	Body struct {
		Settings *EmailAlertSettings `json:"settings,omitempty" doc:"Unsaved settings to test; omitted uses the saved ones."`
		Email    string              `json:"email" minLength:"3"`
		Mode     string              `json:"mode,omitempty" enum:"instant,daily,weekly"`
		Pick     string              `json:"pick,omitempty" enum:"episode,movie" doc:"Send only the newest episode or movie."`
	}
}

type AdminEmailAlertsTestResult struct {
	Subject string `json:"subject"`
	Items   int    `json:"items"`
}

type AdminEmailAlertsTestOutput struct{ Body AdminEmailAlertsTestResult }

type AdminEmailAlertsReportOutput struct{ Body EmailAlertRunReport }

type AdminEmailAlertRecipientBody struct {
	Name        string   `json:"name,omitempty"`
	Email       string   `json:"email,omitempty"`
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode" enum:"instant,instant_daily,instant_weekly,daily,weekly"`
	Content     string   `json:"content,omitempty" enum:"followed,all" doc:"Profile recipients only: shows the profile follows, or everything new."`
	Types       []string `json:"types,omitempty"`
	LibraryIDs  []int    `json:"library_ids,omitempty"`
	MaxAgeHours int      `json:"max_age_hours,omitempty"`
}

func (b AdminEmailAlertRecipientBody) recipient() notifications.EmailAlertRecipient {
	return notifications.EmailAlertRecipient{Name: b.Name, Email: b.Email, Enabled: b.Enabled, Mode: b.Mode,
		Content: b.Content, Types: b.Types, LibraryIDs: b.LibraryIDs, MaxAgeHours: b.MaxAgeHours}
}

type AdminEmailAlertRecipientCreateInput struct {
	Body AdminEmailAlertRecipientBody
}

type AdminEmailAlertRecipientUpdateInput struct {
	Key  string `path:"key" doc:"Recipient key: r:<id> for an added address, p:<user id>:<profile id> for a profile."`
	Body AdminEmailAlertRecipientBody
}

type AdminEmailAlertRecipientKeyInput struct {
	Key string `path:"key"`
}

type AdminEmailAlertRecipientRewindInput struct {
	Key  string `path:"key"`
	Body struct {
		Hours int `json:"hours,omitempty" minimum:"1" maximum:"720" doc:"How far back to resend; defaults to 24."`
	}
}

type AdminEmailAlertRecipientOutput struct{ Body EmailAlertRecipient }

func (reg *Registry) adminEmailAlerts() (AdminEmailAlertsService, *Problem) {
	if reg.deps.AdminEmailAlerts == nil {
		return nil, unavailable("email alerts")
	}
	return reg.deps.AdminEmailAlerts, nil
}

func (reg *Registry) emailAlertsOverview(ctx context.Context, svc AdminEmailAlertsService) (*AdminEmailAlertsStateOutput, error) {
	state, err := svc.State(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	body, err := convertJSON[EmailAlertsOverview](state)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &AdminEmailAlertsStateOutput{Body: body}, nil
}

func emailAlertRecipientOutput(rec notifications.EmailAlertRecipient) (*AdminEmailAlertRecipientOutput, error) {
	body, err := convertJSON[EmailAlertRecipient](rec)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &AdminEmailAlertRecipientOutput{Body: body}, nil
}

func emailAlertsOperation(method, path, id, summary string) Operation {
	op := Operation{Operation: humaOp(method, Prefix+"/admin/notifications/email-alerts"+path, id, "admin", summary), Class: ClassActingAdmin, ServiceBacked: true}
	if method != http.MethodGet {
		op.DemoRestricted = true
		op.RetrySafety = RetrySafetyNonRetryable
	}
	return op
}

func emailRecipientProblem(err error) error {
	switch {
	case errors.Is(err, notifications.ErrEmailRecipientNotFound):
		return NewProblem(TypeNotFound, "Recipient not found.")
	case errors.Is(err, notifications.ErrEmailRecipientExists):
		return NewProblem(TypeConflict, "Another recipient already uses that address.")
	case errors.Is(err, notifications.ErrEmailRecipientInvalid):
		return NewProblem(TypeValidationFailed, err.Error())
	}
	return serviceProblem(err)
}

func registerAdminEmailAlerts(reg *Registry) {
	Register(reg, emailAlertsOperation(http.MethodGet, "", "getAdminEmailAlerts", "Read the email alert settings, recipients, last run and send log."),
		func(ctx context.Context, _ *struct{}) (*AdminEmailAlertsStateOutput, error) {
			svc, p := reg.adminEmailAlerts()
			if p != nil {
				return nil, p
			}
			return reg.emailAlertsOverview(ctx, svc)
		})

	settings := emailAlertsOperation(http.MethodPut, "/settings", "updateAdminEmailAlertSettings", "Save the email templates, design, schedule and delivery settings. Templates that fail to render are rejected.")
	settings.RetrySafety = RetrySafetyNaturalIdempotent
	settings.MaxBodyBytes = 512 << 10
	Register(reg, settings, func(ctx context.Context, in *AdminEmailAlertsSettingsInput) (*AdminEmailAlertsStateOutput, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		s, err := convertJSON[emailalerts.Settings](in.Body.Settings)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, "Settings could not be read.")
		}
		if _, err := svc.SaveSettings(ctx, s); err != nil {
			var tmpl *emailalerts.TemplateError
			if errors.As(err, &tmpl) {
				return nil, NewProblem(TypeValidationFailed, tmpl.Error())
			}
			return nil, serviceProblem(err)
		}
		return reg.emailAlertsOverview(ctx, svc)
	})

	preview := emailAlertsOperation(http.MethodPost, "/preview", "previewAdminEmailAlert", "Render unsaved settings with the newest items or sample content. Sends nothing.")
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	preview.DemoRestricted = false
	preview.MaxBodyBytes = 512 << 10
	Register(reg, preview, func(ctx context.Context, in *AdminEmailAlertsPreviewInput) (*AdminEmailAlertsPreviewOutput, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		s, err := convertJSON[emailalerts.Settings](in.Body.Settings)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, "Settings could not be read.")
		}
		rendered, note, err := svc.Preview(ctx, s, in.Body.Mode, in.Body.Source)
		if err != nil {
			return &AdminEmailAlertsPreviewOutput{Body: AdminEmailAlertsPreview{Error: err.Error(), Note: note}}, nil
		}
		return &AdminEmailAlertsPreviewOutput{Body: AdminEmailAlertsPreview{Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text, Note: note}}, nil
	})

	test := emailAlertsOperation(http.MethodPost, "/test", "sendAdminEmailAlertTest", "Email the newest items to one address. Send once; each call sends an email.")
	test.MaxBodyBytes = 512 << 10
	Register(reg, test, func(ctx context.Context, in *AdminEmailAlertsTestInput) (*AdminEmailAlertsTestOutput, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		var settings *emailalerts.Settings
		if in.Body.Settings != nil {
			s, err := convertJSON[emailalerts.Settings](*in.Body.Settings)
			if err != nil {
				return nil, NewProblem(TypeValidationFailed, "Settings could not be read.")
			}
			settings = &s
		}
		subject, items, err := svc.SendTest(ctx, settings, in.Body.Email, in.Body.Mode, in.Body.Pick)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, err.Error())
		}
		return &AdminEmailAlertsTestOutput{Body: AdminEmailAlertsTestResult{Subject: subject, Items: items}}, nil
	})

	Register(reg, emailAlertsOperation(http.MethodPost, "/run", "runAdminEmailAlerts", "Send every email that is due now."),
		func(ctx context.Context, _ *struct{}) (*AdminEmailAlertsReportOutput, error) {
			svc, p := reg.adminEmailAlerts()
			if p != nil {
				return nil, p
			}
			body, err := convertJSON[EmailAlertRunReport](svc.RunDue(ctx, "run now from the admin page"))
			if err != nil {
				return nil, serviceProblem(err)
			}
			return &AdminEmailAlertsReportOutput{Body: body}, nil
		})

	Register(reg, emailAlertsOperation(http.MethodPost, "/recipients", "createAdminEmailAlertRecipient", "Add an address that receives everything new in the chosen libraries."),
		func(ctx context.Context, in *AdminEmailAlertRecipientCreateInput) (*AdminEmailAlertRecipientOutput, error) {
			svc, p := reg.adminEmailAlerts()
			if p != nil {
				return nil, p
			}
			saved, err := svc.SaveAdminRecipient(ctx, in.Body.recipient())
			if err != nil {
				return nil, emailRecipientProblem(err)
			}
			return emailAlertRecipientOutput(saved)
		})

	update := emailAlertsOperation(http.MethodPut, "/recipients/{key}", "updateAdminEmailAlertRecipient", "Change a recipient. For a profile only the delivery choices change; its address stays the profile's own.")
	update.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, update, func(ctx context.Context, in *AdminEmailAlertRecipientUpdateInput) (*AdminEmailAlertRecipientOutput, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		saved, err := svc.UpdateRecipient(ctx, in.Key, in.Body.recipient())
		if err != nil {
			return nil, emailRecipientProblem(err)
		}
		return emailAlertRecipientOutput(saved)
	})

	remove := emailAlertsOperation(http.MethodDelete, "/recipients/{key}", "deleteAdminEmailAlertRecipient", "Remove an added address, or switch a profile's email off.")
	remove.DefaultStatus = http.StatusNoContent
	remove.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, remove, func(ctx context.Context, in *AdminEmailAlertRecipientKeyInput) (*struct{}, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		if err := svc.DeleteRecipient(ctx, in.Key); err != nil {
			return nil, emailRecipientProblem(err)
		}
		return &struct{}{}, nil
	})

	rewind := emailAlertsOperation(http.MethodPost, "/recipients/{key}/rewind", "rewindAdminEmailAlertRecipient", "Resend the last hours to one recipient on its next send.")
	rewind.DefaultStatus = http.StatusNoContent
	Register(reg, rewind, func(ctx context.Context, in *AdminEmailAlertRecipientRewindInput) (*struct{}, error) {
		svc, p := reg.adminEmailAlerts()
		if p != nil {
			return nil, p
		}
		if err := svc.Rewind(ctx, in.Key, in.Body.Hours); err != nil {
			return nil, serviceProblem(err)
		}
		return &struct{}{}, nil
	})
}
