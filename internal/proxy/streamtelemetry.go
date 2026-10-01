package proxy

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func attachStream(ctx context.Context, claims *streamtoken.Claims) {
	if claims == nil {
		return
	}
	startedAt, startedSource, tokenIssuedAt, tokenSource := playback.TelemetryTokenTiming(claims)
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{
		Subject: streamtelemetry.UserSubject(claims.UserID), ProfileID: claims.ProfileID,
		SessionID: claims.SessionID, MediaFileID: claims.MediaFileID, PlayMethod: claims.PlayMethod,
		StartedAt: startedAt, StartedAtSource: startedSource,
		TokenIssuedAt: tokenIssuedAt, TokenIssuedAtSource: tokenSource,
	})
}

func attachTransfer(ctx context.Context, claims *streamtoken.Claims) {
	if claims == nil {
		return
	}
	startedAt, startedSource, tokenIssuedAt, tokenSource := playback.TelemetryTokenTiming(claims)
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{
		Subject: streamtelemetry.UserSubject(claims.UserID), ProfileID: claims.ProfileID,
		MediaFileID: claims.MediaFileID, StartedAt: startedAt, StartedAtSource: startedSource,
		TokenIssuedAt: tokenIssuedAt, TokenIssuedAtSource: tokenSource,
	})
}
