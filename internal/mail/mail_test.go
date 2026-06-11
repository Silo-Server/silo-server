package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubSettings map[string]string

func (s stubSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func configuredSettings() stubSettings {
	return stubSettings{
		SettingEnabled:     "true",
		SettingSMTPHost:    "smtp.example.com",
		SettingFromAddress: "silo@example.com",
	}
}

func TestLoadConfigGates(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled by default", func(t *testing.T) {
		sender := NewSMTPSender(stubSettings{})
		if sender.Enabled(ctx) {
			t.Fatal("email must be disabled with no settings")
		}
		err := sender.Send(ctx, Message{To: []string{"a@b.c"}, Subject: "x", TextBody: "y"})
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Send = %v, want ErrNotConfigured", err)
		}
	})

	t.Run("requires host and from address", func(t *testing.T) {
		settings := configuredSettings()
		settings[SettingSMTPHost] = ""
		if NewSMTPSender(settings).Enabled(ctx) {
			t.Fatal("missing host must disable email")
		}
		settings = configuredSettings()
		settings[SettingFromAddress] = ""
		if NewSMTPSender(settings).Enabled(ctx) {
			t.Fatal("missing from address must disable email")
		}
	})

	t.Run("complete config enables", func(t *testing.T) {
		if !NewSMTPSender(configuredSettings()).Enabled(ctx) {
			t.Fatal("complete config must enable email")
		}
	})
}

func TestLoadConfigValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("defaults", func(t *testing.T) {
		cfg, err := NewSMTPSender(configuredSettings()).loadConfig(ctx)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.port != 587 || cfg.security != securityStartTLS || cfg.fromName != "Silo" {
			t.Fatalf("unexpected defaults: %+v", cfg)
		}
	})

	t.Run("invalid port", func(t *testing.T) {
		settings := configuredSettings()
		settings[SettingSMTPPort] = "99999"
		if _, err := NewSMTPSender(settings).loadConfig(ctx); err == nil {
			t.Fatal("out-of-range port must be rejected")
		}
	})

	t.Run("invalid security mode", func(t *testing.T) {
		settings := configuredSettings()
		settings[SettingSMTPSecurity] = "plz-hack-me"
		if _, err := NewSMTPSender(settings).loadConfig(ctx); err == nil {
			t.Fatal("unknown security mode must be rejected")
		}
	})
}

func TestSendInputValidation(t *testing.T) {
	ctx := context.Background()
	sender := NewSMTPSender(configuredSettings())

	if err := sender.Send(ctx, Message{Subject: "x", TextBody: "y"}); err == nil {
		t.Fatal("a message without recipients must be rejected")
	}
	if err := sender.Send(ctx, Message{To: []string{"a@b.c"}, Subject: "x"}); err == nil {
		t.Fatal("a message without a body must be rejected")
	}
}

func TestBuildMessageMultipart(t *testing.T) {
	cfg := &smtpConfig{fromAddress: "silo@example.com", fromName: "Silo"}
	message, err := buildMessage(cfg, Message{
		To:       []string{"user@example.com"},
		Subject:  "Hello",
		TextBody: "plain",
		HTMLBody: "<b>rich</b>",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	var rendered strings.Builder
	if _, err := message.WriteTo(&rendered); err != nil {
		t.Fatalf("render message: %v", err)
	}
	output := rendered.String()
	for _, want := range []string{"multipart/alternative", "plain", "rich", "Silo", "user@example.com"} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered message missing %q:\n%s", want, output)
		}
	}
}
