package userstore

import (
	"context"
	"time"
)

// JellycompatProgressEdit is one explicit leaf edit. History is added only for
// an explicit played mark; ClearHistory implements an explicit unplayed mark.
// Progress and the history mutation must commit or roll back together.
type JellycompatProgressEdit struct {
	MediaItemID                      string
	PositionSeconds, DurationSeconds float64
	Completed                        bool
	EventAt                          time.Time
	History                          *WatchHistoryEntry
	ClearHistory                     bool
}

type JellycompatProgressEditor interface {
	ApplyJellycompatProgress(context.Context, string, JellycompatProgressEdit) error
}
