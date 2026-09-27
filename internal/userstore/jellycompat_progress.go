package userstore

import (
	"context"
	"time"
)

// JellycompatProgressEdit is one explicit leaf edit. History is added only for
// an explicit played mark; ClearHistory implements an explicit unplayed mark.
// Progress, history, and an optional favorite mutation commit or roll back together.
type JellycompatProgressEdit struct {
	MediaItemID                      string
	PositionSeconds, DurationSeconds float64
	Completed                        bool
	EventAt                          time.Time
	History                          *WatchHistoryEntry
	ClearHistory                     bool
	IsFavorite                       *bool
}

type JellycompatProgressEditor interface {
	ApplyJellycompatProgress(context.Context, string, JellycompatProgressEdit) error
}

// JellycompatParentEdit commits a parent's favorite and its children's watch
// state together. Targets and history use the existing batch-mark semantics.
type JellycompatParentEdit struct {
	MediaItemID string
	IsFavorite  bool
	Played      bool
	Targets     []MarkWatchedTarget
	History     []WatchHistoryEntry
}

type JellycompatParentEditor interface {
	ApplyJellycompatParent(context.Context, string, JellycompatParentEdit) error
}
