// Package mail provides Silo's shared outbound email facility. It is
// deliberately feature-agnostic: notifications, password resets, invites, and
// any future feature send through the same Sender so SMTP configuration,
// security policy, and diagnostics live in exactly one place.
//
// Configuration is read live from server settings (no restart required):
//
//	email.enabled        bool, default false
//	email.smtp_host      hostname (required to enable)
//	email.smtp_port      default 587
//	email.smtp_security  starttls (default) | tls | none
//	email.smtp_username  optional
//	email.smtp_password  optional; encrypted at rest (SensitiveSettingKeys)
//	email.from_address   required to enable
//	email.from_name      default "Silo"
package mail

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Server-setting keys. email.smtp_password must stay registered in
// catalog.SensitiveSettingKeys so it is encrypted at rest and redacted from
// the admin API.
const (
	SettingEnabled      = "email.enabled"
	SettingSMTPHost     = "email.smtp_host"
	SettingSMTPPort     = "email.smtp_port"
	SettingSMTPSecurity = "email.smtp_security"
	SettingSMTPUsername = "email.smtp_username"
	SettingSMTPPassword = "email.smtp_password"
	SettingFromAddress  = "email.from_address"
	SettingFromName     = "email.from_name"
)

const sendTimeout = 30 * time.Second

// Security modes for email.smtp_security.
const (
	securityStartTLS = "starttls"
	securityTLS      = "tls"
	securityNone     = "none"
)

// ErrNotConfigured is returned by Send when email is disabled or incomplete.
// Callers treat email as an optional transport and degrade gracefully.
var ErrNotConfigured = errors.New("email is not configured")

// Message is one outbound email. At least one body variant is required; when
// both are set the message is sent as multipart/alternative.
type Message struct {
	To       []string
	Subject  string
	TextBody string
	HTMLBody string
	// ReplyTo optionally overrides the reply address.
	ReplyTo string
}

// Sender is the feature-facing abstraction. Implementations must be safe for
// concurrent use.
type Sender interface {
	// Enabled reports whether email is configured and turned on, so features
	// can skip composing messages that could never send.
	Enabled(ctx context.Context) bool
	// Send delivers one message, returning ErrNotConfigured when email is off.
	Send(ctx context.Context, msg Message) error
}

// SettingReader reads live server settings. Satisfied by
// catalog.EncryptedSettingsRepo (which transparently decrypts the password).
type SettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// SMTPSender sends through a user-configured SMTP server. Settings are read
// on every send: email volume is low (notifications, account flows) and live
// reads mean admin changes apply without a restart.
type SMTPSender struct {
	settings SettingReader
}

// NewSMTPSender creates the shared SMTP sender.
func NewSMTPSender(settings SettingReader) *SMTPSender {
	return &SMTPSender{settings: settings}
}

type smtpConfig struct {
	host        string
	port        int
	security    string
	username    string
	password    string
	fromAddress string
	fromName    string
}

func (s *SMTPSender) loadConfig(ctx context.Context) (*smtpConfig, error) {
	if s == nil || s.settings == nil {
		return nil, ErrNotConfigured
	}
	// A settings-store failure must surface as an error, never be mistaken
	// for "email is not configured" — that would silently hide real backend
	// problems behind a graceful-degradation path.
	var readErr error
	get := func(key string) string {
		value, err := s.settings.Get(ctx, key)
		if err != nil && readErr == nil {
			readErr = fmt.Errorf("read setting %s: %w", key, err)
		}
		return strings.TrimSpace(value)
	}
	enabled := truthy(get(SettingEnabled))
	if readErr != nil {
		return nil, readErr
	}
	if !enabled {
		return nil, ErrNotConfigured
	}
	cfg := &smtpConfig{
		host:        get(SettingSMTPHost),
		port:        587,
		security:    strings.ToLower(get(SettingSMTPSecurity)),
		username:    get(SettingSMTPUsername),
		password:    get(SettingSMTPPassword),
		fromAddress: get(SettingFromAddress),
		fromName:    get(SettingFromName),
	}
	portRaw := get(SettingSMTPPort)
	if readErr != nil {
		return nil, readErr
	}
	if cfg.host == "" || cfg.fromAddress == "" {
		return nil, ErrNotConfigured
	}
	if raw := portRaw; raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid email.smtp_port %q", raw)
		}
		cfg.port = port
	}
	switch cfg.security {
	case "":
		cfg.security = securityStartTLS
	case securityStartTLS, securityTLS, securityNone:
	default:
		return nil, fmt.Errorf("invalid email.smtp_security %q", cfg.security)
	}
	if cfg.fromName == "" {
		cfg.fromName = "Silo"
	}
	return cfg, nil
}

// Enabled reports whether email can send right now.
func (s *SMTPSender) Enabled(ctx context.Context) bool {
	_, err := s.loadConfig(ctx)
	return err == nil
}

// Send delivers one message over SMTP.
func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	if len(msg.To) == 0 {
		return errors.New("email message has no recipients")
	}
	if msg.TextBody == "" && msg.HTMLBody == "" {
		return errors.New("email message has no body")
	}

	message, err := buildMessage(cfg, msg)
	if err != nil {
		return err
	}
	client, err := newClient(cfg)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := client.DialAndSendWithContext(sendCtx, message); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

func buildMessage(cfg *smtpConfig, msg Message) (*gomail.Msg, error) {
	message := gomail.NewMsg()
	if err := message.FromFormat(cfg.fromName, cfg.fromAddress); err != nil {
		return nil, fmt.Errorf("invalid from address: %w", err)
	}
	if err := message.To(msg.To...); err != nil {
		return nil, fmt.Errorf("invalid recipient: %w", err)
	}
	if msg.ReplyTo != "" {
		if err := message.ReplyTo(msg.ReplyTo); err != nil {
			return nil, fmt.Errorf("invalid reply-to address: %w", err)
		}
	}
	message.Subject(msg.Subject)
	switch {
	case msg.HTMLBody != "" && msg.TextBody != "":
		message.SetBodyString(gomail.TypeTextPlain, msg.TextBody)
		message.AddAlternativeString(gomail.TypeTextHTML, msg.HTMLBody)
	case msg.HTMLBody != "":
		message.SetBodyString(gomail.TypeTextHTML, msg.HTMLBody)
	default:
		message.SetBodyString(gomail.TypeTextPlain, msg.TextBody)
	}
	return message, nil
}

func newClient(cfg *smtpConfig) (*gomail.Client, error) {
	options := []gomail.Option{
		gomail.WithPort(cfg.port),
		gomail.WithTimeout(sendTimeout),
	}
	switch cfg.security {
	case securityTLS: // implicit TLS (typically port 465)
		options = append(options, gomail.WithSSL())
	case securityNone:
		options = append(options, gomail.WithTLSPolicy(gomail.NoTLS))
	default: // starttls
		options = append(options, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if cfg.username != "" {
		options = append(options,
			gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(cfg.username),
			gomail.WithPassword(cfg.password),
		)
	}
	return gomail.NewClient(cfg.host, options...)
}

func truthy(value string) bool {
	switch strings.ToLower(value) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}
