package metadata

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// seasonBackdropAuthority says how far a season write may decide a season's
// backdrop. The backdrop is provider-owned like the poster, but a provider
// that offers none clears it, so only a refresh that heard from every season
// provider may write it. Anything less keeps the stored backdrop.
type seasonBackdropAuthority uint8

const (
	// seasonBackdropsKept keeps every stored backdrop. It is the default: a
	// local-only reapply, or a refresh where a season provider failed.
	seasonBackdropsKept seasonBackdropAuthority = iota
	// seasonBackdropsAnswered writes the backdrop of each season a provider
	// returned, clearing it when none offered one.
	seasonBackdropsAnswered
	// seasonBackdropsReidentified also clears the backdrop of a season only
	// episodes reference: after Identify it may show the previous match.
	seasonBackdropsReidentified
)

type seasonBackdropAuthorityKey struct{}

func withSeasonBackdropAuthority(ctx context.Context, authority seasonBackdropAuthority) context.Context {
	if authority == seasonBackdropsKept {
		return ctx
	}
	return context.WithValue(ctx, seasonBackdropAuthorityKey{}, authority)
}

func seasonBackdropAuthorityOf(ctx context.Context) seasonBackdropAuthority {
	authority, _ := ctx.Value(seasonBackdropAuthorityKey{}).(seasonBackdropAuthority)
	return authority
}

// seasonBackdropAuthorityFor is the authority of a refresh in mode whose
// season providers all answered when complete holds.
func seasonBackdropAuthorityFor(mode RefreshMode, complete bool) seasonBackdropAuthority {
	switch {
	case !complete:
		return seasonBackdropsKept
	case mode == ModeIdentify:
		return seasonBackdropsReidentified
	default:
		return seasonBackdropsAnswered
	}
}

// setProviderSeasonBackdrop marks season to write the backdrop offered, a
// provider path or empty. The stored copy stays while it is a cached copy of
// the same source and keepStored holds; a different source replaces it at
// once, so a corrected match never keeps showing the previous show's art
// while the new image downloads. Under the series' Images lock a stored
// backdrop stays, so season is left unmarked.
func setProviderSeasonBackdrop(season, existing *models.Season, offered string, imagesLocked, keepStored bool) {
	if imagesLocked && existing != nil && existing.BackdropPath != "" {
		return
	}
	season.WriteBackdrop = true
	offered = strings.TrimSpace(offered)
	if !isRemoteImageSourcePath(offered) {
		season.BackdropPath, season.BackdropSourcePath, season.BackdropThumbhash = "", "", ""
		return
	}
	if existing != nil && existing.BackdropSourcePath == offered &&
		(existing.BackdropPath == offered || (keepStored && isCachedImagePath(existing.BackdropPath))) {
		season.BackdropPath, season.BackdropSourcePath, season.BackdropThumbhash = existing.BackdropPath, offered, existing.BackdropThumbhash
		return
	}
	season.BackdropPath, season.BackdropSourcePath, season.BackdropThumbhash = offered, offered, ""
}
