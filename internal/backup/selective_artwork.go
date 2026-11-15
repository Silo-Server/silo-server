package backup

import (
	"context"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Item artwork and branding, the collections section's items that restore
// stored objects.

func (a *applier) artworkEntry(id string) *ItemArtworkEntry {
	return indexed(a.rs.Collections.ItemArtwork, a.rs.lookup().artwork, id)
}

// artworkThumbhash is the thumbhash of the restored original, as the image
// cache computes one when it stores artwork; the backup's own is used only
// when the image cannot be read, and only when it is one (validThumbhash).
func (a *applier) artworkThumbhash(entry ItemArtworkEntry) string {
	if thumb, err := a.blobThumbhash(entry.Path); err == nil {
		return thumb
	}
	return thumbFor(entry.Path, entry.Thumbhash)
}

// blobThumbhash computes the thumbhash of an extracted image.
func (a *applier) blobThumbhash(key string) (string, error) {
	f, size, err := a.rs.openBlob(key)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if size > maxRestoredFile {
		return "", ErrRestoredFileRefused
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return imageutil.Thumbhash(data)
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
	a.filesWritten[id] = plan.Reason == ReasonFilesMissingHere && a.staged[id] == nil
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
	if a.filesWritten[id] && plan.Reason == ReasonSame {
		// Its files are back; the catalog already points at them.
		item.Outcome = OutcomeRestored
		return applied{item: item}, nil
	}
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
		ImageType: entry.ImageType, StoredPath: entry.Path, SourcePath: entry.Source, Thumbhash: a.artworkThumbhash(*entry),
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
	entry := indexed(a.rs.Collections.Branding, a.rs.lookup().branding, id)
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
	plan, err := a.s.newPlanner(a.rs, tx).planBranding(ctx, stored, *entry)
	if err != nil {
		return applied{}, err
	}
	if !plan.Selectable {
		return leftOutFor(item, plan), nil
	}
	if a.s.targets.ApplySettings == nil {
		return applied{}, errors.New("settings restore is not available on this server")
	}
	if err := a.writeBlob(ctx, entry.Blob, entry.Blob); err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	if plan.Reason == ReasonFilesMissingHere {
		// The setting already names the file just written back.
		return applied{item: item}, nil
	}
	_, after, err := a.s.targets.ApplySettings(ctx, tx, map[string]string{entry.Key: entry.Ref}, nil)
	if err != nil {
		return applied{}, err
	}
	return applied{item: item, after: after}, nil
}
