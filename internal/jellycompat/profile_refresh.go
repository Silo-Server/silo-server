package jellycompat

import "context"

// signalsChangedNotifier records that a profile's recommendation signals
// changed, so its taste profile and cached recommendations get rebuilt.
// *recommendations.Worker implements it.
type signalsChangedNotifier interface {
	NotifySignalsChanged(ctx context.Context, userID int, profileID string)
}

func notifySignalsChanged(ctx context.Context, notifier signalsChangedNotifier, userID int, profileID string) {
	if notifier != nil {
		notifier.NotifySignalsChanged(ctx, userID, profileID)
	}
}
