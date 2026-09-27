package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const testAdminDiscordNotificationOperation = "testAdminDiscordNotification"

// AdminNotificationDiscordService verifies the stored bot credential without
// sending a message or changing the account's linked Discord identity.
type AdminNotificationDiscordService interface {
	TestDiscordBot(context.Context) (discord.User, error)
}

type AdminNotificationDiscordTestResult struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms" minimum:"0"`
	Message    string `json:"message"`
}
type AdminNotificationDiscordTestOutput struct {
	Body AdminNotificationDiscordTestResult
}

func registerAdminNotificationDiscord(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/notifications/discord/test", testAdminDiscordNotificationOperation, "admin", "Verify the stored Discord bot credential without sending a message."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, _ *struct{}) (*AdminNotificationDiscordTestOutput, error) {
		if reg.deps.AdminNotificationDiscord == nil {
			return nil, unavailable("Discord bot verification")
		}
		start := time.Now()
		bot, err := reg.deps.AdminNotificationDiscord.TestDiscordBot(ctx)
		result := AdminNotificationDiscordTestResult{OK: err == nil, DurationMS: time.Since(start).Milliseconds()}
		switch {
		case errors.Is(err, notifications.ErrDiscordNotConfigured):
			result.Message = "Bot token is not configured"
		case err != nil:
			result.Message = "Could not verify the Discord bot credential"
		default:
			result.Message = "Connected as " + bot.Username
		}
		return &AdminNotificationDiscordTestOutput{Body: result}, nil
	})
}
