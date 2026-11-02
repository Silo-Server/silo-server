package backup

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Item artwork and branding, the collections section's items that restore
// stored objects.

func (a *applier) artworkEntry(id string) *ItemArtworkEntry {
	for i := range a.rs.Collections.ItemArtwork {
		if a.rs.Collections.ItemArtwork[i].itemID() == id {
			return &a.rs.Collections.ItemArtwork[i]
		}
	}
	return nil
}

// stageArtwork writes an artwork item's objects before its transaction,
// in the image cache's order: the revision is registered with the
// collector, its objects written, and then their exact manifest recorded,
// so the collector never reclaims objects a restore is about to publish.
// It runs outside the item's transaction because the tracker writes on a
// pool connection of its own, which must commit before the collector can
// see it. Objects of an item the transaction then leaves out stay
// registered, and the collector reclaims them.
func (a *applier) stageArtwork(ctx context.Context, id string) {
	entry := a.artworkEntry(id)
	if entry == nil {
		return
	}
	plan, err := a.s.newPlanner(a.rs, a.s.opts.Pool).planArtwork(ctx, *entry)
	if err != nil {
		a.staged[id] = err
		return
	}
	if !plan.Selectable {
		return
	}
	a.staged[id] = a.writeArtwork(ctx, *entry)
}

// writeArtwork registers and writes an artwork entry's objects.
func (a *applier) writeArtwork(ctx context.Context, entry ItemArtworkEntry) error {
	track := a.s.targets.TrackArtwork
	if track != nil {
		if err := track(ctx, entry.Path, entry.ImageType, nil); err != nil {
			return err
		}
	}
	// Content ids, and so the image's keys, are the same on every server.
	keys := a.rs.blobFamily(entry.Path)
	for _, key := range keys {
		if err := a.writeBlob(ctx, key, key); err != nil {
			return err
		}
	}
	if track != nil {
		return track(ctx, entry.Path, entry.ImageType, keys)
	}
	return nil
}

func (a *applier) applyArtwork(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindItemArtwork, Label: id}
	entry := a.artworkEntry(id)
	if entry == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such image."), nil
	}
	plan, err := a.s.newPlanner(a.rs, tx).planArtwork(ctx, *entry)
	if err != nil {
		return applied{}, err
	}
	item.Label = plan.Label
	if !plan.Selectable {
		return leftOutFor(item, plan), nil
	}
	stageErr, staged := a.staged[id]
	if !staged {
		return leftOut(item, ReasonChanged, "The image changed while the restore ran. Open the backup again."), nil
	}
	if stageErr != nil {
		return applied{}, stageErr
	}
	if err := catalog.PublishArtworkSelectionInTx(ctx, tx, catalog.ArtworkSelection{
		TargetType: entry.TargetType, TargetContentID: entry.ContentID, ParentContentID: entry.ParentContentID,
		ImageType: entry.ImageType, StoredPath: entry.Path, SourcePath: entry.Source, Thumbhash: entry.Thumbhash,
		LockField: int(metadata.FieldImages),
	}); err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	announce, content, parent := a.s.targets.ArtworkChanged, entry.ContentID, entry.ParentContentID
	return applied{item: item, after: func(ctx context.Context) {
		if announce != nil {
			announce(ctx, content, parent)
		}
	}}, nil
}

func (a *applier) applyBranding(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindBranding, Label: id}
	var entry *BrandingEntry
	for i := range a.rs.Collections.Branding {
		if a.rs.Collections.Branding[i].itemID() == id {
			entry = &a.rs.Collections.Branding[i]
		}
	}
	if entry == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such branding asset."), nil
	}
	item.Label = entry.Key
	// The parser admitted it, but the setting this writes is decided here,
	// so the check runs again where it matters.
	if err := validBrandingEntry(*entry); err != nil {
		return applied{}, err
	}
	stored, err := readStoredSettings(ctx, tx)
	if err != nil {
		return applied{}, err
	}
	plan := a.s.newPlanner(a.rs, tx).planBranding(stored, *entry)
	if !plan.Selectable {
		return leftOutFor(item, plan), nil
	}
	if a.s.targets.ApplySettings == nil {
		return applied{}, errors.New("settings restore is not available on this server")
	}
	if err := a.writeBlob(ctx, entry.Blob, entry.Blob); err != nil {
		return applied{}, err
	}
	_, after, err := a.s.targets.ApplySettings(ctx, tx, map[string]string{entry.Key: entry.Ref})
	if err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	return applied{item: item, after: after}, nil
}
