package apiv2

import (
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func watchlistRows() []userstore.WatchlistEntry {
	return []userstore.WatchlistEntry{
		{ProfileID: "p-owner", MediaItemID: "series:c", AddedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", MediaItemID: "series:b", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "series:a", AddedAt: "2025-12-31T00:00:00Z"},
		{ProfileID: "p-other", MediaItemID: "series:z", AddedAt: "2025-12-30T00:00:00Z"},
	}
}
