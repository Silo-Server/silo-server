package jellycompat

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
)

// userViewOrder is the profile's saved view order. libraryOrder is the
// profile's native ui.library_order; orderedViews is the Jellyfin preference
// that clients post in the user configuration. Libraries take the native
// order first, then OrderedViews moves the views it names to the front, so
// views it doesn't list still follow the profile's own order.
type userViewOrder struct {
	libraryOrder []string
	orderedViews []string
}

// loadUserViewOrder reads the session profile's view order. A read failure
// leaves the default order: browsing must not fail on a preference.
func (h *ItemsHandler) loadUserViewOrder(ctx context.Context, session *Session) userViewOrder {
	var order userViewOrder
	if h.storeProvider == nil || session == nil || session.ProfileID == "" {
		return order
	}
	store, err := h.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil || store == nil {
		slog.WarnContext(ctx, "jellycompat view order: user store unavailable", "component", "jellycompat", "error", err)
		return order
	}
	if raw, err := store.GetSetting(ctx, configurationKey(session.ProfileID)); err != nil {
		slog.WarnContext(ctx, "jellycompat view order: read configuration failed", "component", "jellycompat", "error", err)
	} else if raw != "" {
		var saved struct{ OrderedViews []string }
		if json.Unmarshal([]byte(raw), &saved) == nil {
			order.orderedViews = saved.OrderedViews
		}
	}
	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "jellycompat view order: load settings contract failed", "component", "jellycompat", "error", err)
		return order
	}
	values, err := settingsresolve.New(contract).Resolve(ctx, store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.UiLibraryOrder}, nil)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat view order: resolve library order failed", "component", "jellycompat", "error", err)
		return order
	}
	var ids []int
	if len(values) == 1 && json.Unmarshal(values[0].Value, &ids) == nil {
		for _, id := range ids {
			order.libraryOrder = append(order.libraryOrder, strconv.Itoa(id))
		}
	}
	return order
}

// sortByPreferredOrder stably moves the values named in order to the front, in
// that order; the rest keep their relative order after them. This is
// Jellyfin's UserViewManager rule for OrderedViews. IDs match without regard to
// case or dashes, since Jellyfin stores view IDs in dashless form.
func sortByPreferredOrder[T any](values []T, order []string, id func(T) string) {
	if len(order) == 0 {
		return
	}
	position := make(map[string]int, len(order))
	for i, raw := range order {
		key := normalizeViewOrderID(raw)
		if _, seen := position[key]; !seen {
			position[key] = i
		}
	}
	rank := func(value T) int {
		if i, ok := position[normalizeViewOrderID(id(value))]; ok {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(values, func(a, b T) int { return cmp.Compare(rank(a), rank(b)) })
}

func normalizeViewOrderID(id string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
}
