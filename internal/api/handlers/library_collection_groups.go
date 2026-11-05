package handlers

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type LibraryCollectionGroupHandler struct {
	groupRepo *catalog.LibraryCollectionGroupRepository
	collRepo  *catalog.LibraryCollectionRepository
	pool      *pgxpool.Pool
}

func NewLibraryCollectionGroupHandler(
	groupRepo *catalog.LibraryCollectionGroupRepository,
	collRepo *catalog.LibraryCollectionRepository,
	pool *pgxpool.Pool,
) *LibraryCollectionGroupHandler {
	return &LibraryCollectionGroupHandler{groupRepo: groupRepo, collRepo: collRepo, pool: pool}
}

type createGroupRequest struct {
	Name            string  `json:"name"`
	Slug            *string `json:"slug,omitempty"`
	DefaultSortMode *string `json:"default_sort_mode,omitempty"`
}

type updateGroupRequest struct {
	Name            *string `json:"name,omitempty"`
	Slug            *string `json:"slug,omitempty"`
	DefaultSortMode *string `json:"default_sort_mode,omitempty"`
}

type listGroupsResponse struct {
	Groups             []libraryCollectionGroupResponse `json:"groups"`
	UngroupedSortOrder int                              `json:"ungrouped_sort_order"`
}

type collectionGroupReorderRequest struct {
	IDs []string `json:"ids"`
}
