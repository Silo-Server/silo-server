package usercollections

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionDefinition is the part of a stored collection that decides which
// titles it shows, and so which collage it gets.
func CollectionDefinition(c userstore.Collection) catalog.PersonalCollectionDefinition {
	return catalog.PersonalCollectionDefinition{ID: c.ID, CollectionType: c.CollectionType, QueryDefinition: c.QueryDefinition, DisplayQueryDefinition: c.DisplayQueryDefinition}
}

// RefreshCollage builds, in the background, the collage the viewer described
// by access sees for c, so it is ready before the next read. It does nothing
// when collages is nil, when c has an uploaded or imported poster, or when
// account userID's collections are kept outside Postgres, where collections
// have no collage.
func RefreshCollage(collages *catalog.PersonalCollectionCollages, store userstore.UserStore, userID int, c *userstore.Collection, access catalog.AccessFilter) {
	if collages == nil || c == nil || strings.TrimSpace(c.PosterURL) != "" || !userstore.HasCatalogSQLState(store) {
		return
	}
	collages.Refresh(userID, CollectionDefinition(*c), access)
}
