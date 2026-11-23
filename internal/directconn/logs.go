package directconn

import (
	"context"
	"log/slog"
)

type logKey struct {
	enabled                         bool
	feature, state, reason, granted string
}

func (s *Service) logFeatures(ctx context.Context, enabled bool, features []Feature) {
	for _, f := range features {
		if !enabled && f.Feature == featureDirect {
			f.State = stateOff
			f.Reason = reasonDisabled
		}
		key := logKey{enabled, f.Feature, f.State, f.Reason, f.Granted}
		if s.logged[f.Feature] == key {
			continue
		}
		level := slog.LevelInfo
		if !enabled && f.Feature != featureDirect {
			level = slog.LevelDebug
		}
		s.cfg.Logger.LogAttrs(ctx, level, "direct connections: feature state",
			slog.String("component", "directconn"), slog.String("host", "api"),
			slog.String("feature", f.Feature), slog.String("kind", f.Kind), slog.String("state", f.State),
			slog.String("reason", f.Reason), slog.String("requested", f.Requested), slog.String("granted", f.Granted),
			slog.String("detail", f.Detail))
		s.logged[f.Feature] = key
	}
}
