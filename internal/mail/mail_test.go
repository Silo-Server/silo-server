package mail

import (
	"context"
	"errors"
	"net"
	"strconv"
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

// A mail server that cannot be reached certainly received nothing, so the
// error says so; callers may then retract whatever the message carried.
func TestSendReportsUnreachableServerAsNotSent(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	sender := NewSMTPSender(stubSettings{
		SettingEnabled:      "true",
		SettingSMTPHost:     "127.0.0.1",
		SettingSMTPPort:     strconv.Itoa(port),
		SettingSMTPSecurity: "none",
		SettingFromAddress:  "silo@example.test",
	})
	err = sender.Send(t.Context(), Message{To: []string{"user@example.test"}, Subject: "s", TextBody: "b"})
	if !errors.Is(err, ErrNotSent) {
		t.Fatalf("send to a closed port = %v, want ErrNotSent", err)
	}
}
