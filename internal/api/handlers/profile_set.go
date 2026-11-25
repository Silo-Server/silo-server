package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ErrProfileSetStale refuses a set that is not newer than the one this
// server holds: an older version, the same version with other content, a
// repeat whose PINs differ from the ones stored, or a set built without the
// person's latest edits on this server.
var ErrProfileSetStale = errors.New("the profile set on this server is newer")

// errProfileSetRetry is a household that changed between the read the set
// was planned on and the transaction; the apply starts over.
var errProfileSetRetry = errors.New("the household changed while the set was applied")

// profileSetAttempts bounds how often an apply starts over.
const profileSetAttempts = 3

// SQLSTATEs of a transaction PostgreSQL aborted so another could go on.
const (
	sqlstateDeadlockDetected     = "40P01"
	sqlstateSerializationFailure = "40001"
)

// profileSetRetryable reports an attempt that starts over: the household
// changed under the plan, or PostgreSQL aborted the set's transaction as a
// deadlock victim. A set locks profile rows before the account row, as a
// profile save does; deleting the account locks them the other way round,
// so the two can deadlock. The set then retries, and a retry after the
// delete committed finds the account gone (ErrProfileSetAccountGone).
func profileSetRetryable(err error) bool {
	if errors.Is(err, errProfileSetRetry) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == sqlstateDeadlockDetected || pgErr.Code == sqlstateSerializationFailure)
}

// ProfileSetCommand is a profile set with its sender already reduced to an
// account: a verified signed message, or a login session that passed the
// session rule.
type ProfileSetCommand struct {
	UserID int
	// Set and Pairs are the set and its optional pairs, verbatim.
	Set   json.RawMessage
	Pairs json.RawMessage
	// EditsSeen is the edits token the sender read; "" when it sent none.
	EditsSeen string
	// Signed marks a set the person key signed. An unsigned set comes from a
	// login session acting as ActiveProfileID, which must be the
	// household's main profile, verified by VerifyProfile.
	Signed          bool
	ActiveProfileID string
	VerifyProfile   func(*userstore.Profile) error
}

// ProfileSetMember is one profile the account holds from the set.
type ProfileSetMember struct {
	SyncID string
	ID     string
}

// ProfileSetSkippedSetting is a known setting whose value the server
// refused; it kept its own.
type ProfileSetSkippedSetting struct {
	SyncID string
	Key    string
}

// ProfileSetResult is the answer to an applied set, as the account now
// holds it.
type ProfileSetResult struct {
	Version           int64
	Profiles          []ProfileSetMember
	LeftOff           []string
	AwaitingLibraries []string
	AwaitingPictures  []string
	AwaitingPins      []string
	SkippedSettings   []ProfileSetSkippedSetting
	// JellyfinBareNameSignIn is whether the bare account name signs in on
	// the Jellyfin-compatible surface as the profiles now stand; nil while
	// that surface is off.
	JellyfinBareNameSignIn *bool
}

// ProfileSetHeldItem is one profile of the set as this server holds it.
type ProfileSetHeldItem struct {
	SyncID             string
	ID                 string
	Position           int
	Name               string
	Picture            string
	HasPIN             bool
	IsChild            bool
	MaxContentRating   string
	MaxAdvisoryAge     int
	RequireAdvisoryAge bool
	RequestsBlocked    bool
	Settings           map[string]json.RawMessage
}

// ProfileSetLocal is a profile on the account outside the set.
type ProfileSetLocal struct {
	ID     string
	Name   string
	HasPIN bool
}

// ProfileSetHeld is the set as this server holds it.
type ProfileSetHeld struct {
	Version    int64
	Items      []ProfileSetHeldItem
	LocalOnly  []ProfileSetLocal
	EditsToken string
	// JellyfinBareNameSignIn is as on ProfileSetResult.
	JellyfinBareNameSignIn *bool
}

// ProfileSetSupported reports whether this server applies profile sets: only
// on the PostgreSQL user store.
func (h *ProfileHandler) ProfileSetSupported() bool { return h.ProfileSetAvailable }

// profileSetStore opens the account's store as a profile set store.
func (h *ProfileHandler) profileSetStore(ctx context.Context, userID int) (userstore.UserStore, userstore.ProfileSetStore, error) {
	if !h.ProfileSetSupported() {
		return nil, nil, userstore.ErrProfileSetUnsupported
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	setStore, ok := store.(userstore.ProfileSetStore)
	if !ok {
		return nil, nil, userstore.ErrProfileSetUnsupported
	}
	return store, setStore, nil
}

// HeldProfileSet reads the account's set as this server holds it. An
// account that never took one answers version 0, no items, and every
// profile in LocalOnly.
func (h *ProfileHandler) HeldProfileSet(ctx context.Context, userID int) (ProfileSetHeld, error) {
	store, setStore, err := h.profileSetStore(ctx, userID)
	if err != nil {
		return ProfileSetHeld{}, err
	}
	snap, err := setStore.ReadProfileSet(ctx)
	if err != nil {
		return ProfileSetHeld{}, err
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return ProfileSetHeld{}, err
	}
	held := ProfileSetHeld{Version: snap.State.Version, EditsToken: profileSetEditsToken(snap)}
	rows := slices.Clone(snap.Rows)
	slices.SortStableFunc(rows, func(a, b userstore.ProfileSetRow) int { return a.SyncPosition - b.SyncPosition })
	for i := range rows {
		row := &rows[i]
		if row.SyncID == "" {
			continue
		}
		picture, err := h.profileSetPicture(ctx, store, &row.Profile)
		if err != nil {
			return ProfileSetHeld{}, err
		}
		held.Items = append(held.Items, ProfileSetHeldItem{
			SyncID: row.SyncID, ID: row.ID, Position: row.SyncPosition, Name: row.Name, Picture: picture,
			HasPIN: row.PINHash != "", IsChild: row.IsChild, MaxContentRating: row.MaxContentRating,
			MaxAdvisoryAge: row.MaxAdvisoryAge, RequireAdvisoryAge: row.RequireAdvisoryAge, RequestsBlocked: row.RequestsBlocked,
			Settings: profileSetHeldSettings(contract, snap.Settings[row.ID]),
		})
	}
	for _, row := range snap.Rows {
		if row.SyncID == "" {
			held.LocalOnly = append(held.LocalOnly, ProfileSetLocal{ID: row.ID, Name: row.Name, HasPIN: row.PINHash != ""})
		}
	}
	held.JellyfinBareNameSignIn = h.jellyfinBareNameSignIn(ctx, userID, snap.Rows)
	return held, nil
}

// jellyfinBareNameSignIn is whether signing in on the Jellyfin-compatible
// surface with the bare account name selects one of these profiles
// (jellycompat.BareNameSelectsProfile); nil while that surface is off or
// the account can't be read.
func (h *ProfileHandler) jellyfinBareNameSignIn(ctx context.Context, userID int, rows []userstore.ProfileSetRow) *bool {
	if !h.JellyfinCompatEnabled || h.UserRepo == nil {
		return nil
	}
	user, err := h.UserRepo.GetByID(ctx, userID)
	if err != nil || user == nil {
		return nil
	}
	profiles := make([]jellycompat.SignInProfile, len(rows))
	for i, row := range rows {
		profiles[i] = jellycompat.SignInProfile{Name: row.Name, HasPIN: row.PINHash != ""}
	}
	selects := jellycompat.BareNameSelectsProfile(user.Username, profiles)
	return &selects
}

// profileSetEditsToken names the person's own edits on this server that no
// accepted set has settled yet, "" when there are none. Nothing records
// such edits yet, so it is always "": the change that records them derives
// the token from them.
func profileSetEditsToken(userstore.ProfileSetSnapshot) string { return "" }

// profileSetPicture is a profile's picture as a set names it: preset:<id>,
// sha256:<hex> of an upload, or "" for none. An upload stored before hashes
// were kept is labeled with the hash of its stored original, once.
func (h *ProfileHandler) profileSetPicture(ctx context.Context, store userstore.UserStore, p *userstore.Profile) (string, error) {
	switch {
	case p.Avatar == "":
		return "", nil
	case isUploadedAvatarRef(p.Avatar):
		label, err := h.profileAvatarSourceSHA256(ctx, store, *p)
		if err != nil {
			return "", err
		}
		p.AvatarSourceSHA256 = label
		if label == "" {
			return "", nil
		}
		return profileSetPicturePrefixHash + label, nil
	}
	if preset, err := normalizePresetAvatarReference(p.Avatar); err == nil {
		return preset, nil
	}
	// A stored preset this server no longer knows shows no picture.
	return "", nil
}

// ApplyProfileSet makes the account's household match the set, in one
// transaction (docs/architecture/app-keys.md, "The profile set").
func (h *ProfileHandler) ApplyProfileSet(ctx context.Context, cmd ProfileSetCommand) (ProfileSetResult, error) {
	doc, err := parseProfileSet(cmd.Set, cmd.Pairs, cmd.EditsSeen)
	if err != nil {
		return ProfileSetResult{}, err
	}
	store, setStore, err := h.profileSetStore(ctx, cmd.UserID)
	if err != nil {
		return ProfileSetResult{}, err
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return ProfileSetResult{}, err
	}
	settings := make([]profileSetSettings, len(doc.Items))
	for i, item := range doc.Items {
		if settings[i], err = planProfileSetSettings(contract, item); err != nil {
			return ProfileSetResult{}, err
		}
	}
	limit, limitErr := h.householdProfileLimit(ctx, cmd.UserID)
	if limitErr != nil {
		return ProfileSetResult{}, limitErr
	}
	run := &profileSetRun{h: h, cmd: cmd, doc: doc, store: store, setStore: setStore, contract: contract, settings: settings, limit: limit}
	for range profileSetAttempts {
		if err := run.attempt(ctx); !profileSetRetryable(err) {
			if err != nil {
				return ProfileSetResult{}, err
			}
			run.afterCommit(ctx)
			return run.result(ctx)
		}
	}
	return ProfileSetResult{}, apiError(http.StatusConflict, "conflict", "The profiles changed while the set was applied; send it again")
}

// requireSetPrimary is the session form's authority: the caller acts as the
// household's main profile, verified with its PIN where it has one. Unlike
// the household manager rule, naming no profile never passes.
func requireSetPrimary(rows []userstore.ProfileSetRow, activeID string, verify func(*userstore.Profile) error) (*userstore.Profile, error) {
	forbidden := apiError(http.StatusForbidden, "forbidden", "A profile set is sent by the household's main profile")
	if activeID == "" {
		return nil, forbidden
	}
	for i := range rows {
		row := &rows[i].Profile
		if row.ID != activeID {
			continue
		}
		if !row.IsPrimary {
			return nil, forbidden
		}
		if row.PINHash == "" {
			return row, nil
		}
		if verify == nil {
			return nil, access.ErrProfileUnverified
		}
		if err := verify(row); err != nil {
			return nil, err
		}
		return row, nil
	}
	return nil, forbidden
}

// heldPicture reports whether a profile already shows the picture an item
// names, and whether this server can show it at all. A hash is matched
// against the upload's stored label only, never against bytes; an unknown
// preset, or a hash this server holds no upload for, leaves the profile's
// picture as it is until the picture arrives.
func heldPicture(row *userstore.Profile, picture string) (target string, held, available bool) {
	switch {
	case picture == "":
		return "", row.Avatar == "", true
	case strings.HasPrefix(picture, profileSetPicturePrefixHash):
		label := strings.TrimPrefix(picture, profileSetPicturePrefixHash)
		ok := isUploadedAvatarRef(row.Avatar) && row.AvatarSourceSHA256 == label
		return row.Avatar, ok, ok
	}
	preset, err := normalizePresetAvatarReference(picture)
	if err != nil {
		return row.Avatar, false, false
	}
	current, _ := normalizePresetAvatarReference(row.Avatar)
	return preset, current == preset && !isUploadedAvatarRef(row.Avatar), true
}

// profileSetAwaitingPin reads the awaiting_pin mark of an applied record.
func profileSetAwaitingPin(applied json.RawMessage) bool {
	if len(applied) == 0 {
		return false
	}
	var record struct {
		AwaitingPin bool `json:"awaiting_pin"`
	}
	_ = json.Unmarshal(applied, &record)
	return record.AwaitingPin
}

// buildProfileSetResult describes the account as the set left it.
func buildProfileSetResult(doc *profileSetDoc, snap userstore.ProfileSetSnapshot, settings []profileSetSettings) ProfileSetResult {
	out := ProfileSetResult{Version: snap.State.Version}
	bySyncID := map[string]*userstore.ProfileSetRow{}
	for i := range snap.Rows {
		if snap.Rows[i].SyncID != "" {
			bySyncID[snap.Rows[i].SyncID] = &snap.Rows[i]
		}
	}
	for i, item := range doc.Items {
		row := bySyncID[item.SyncID]
		if row == nil {
			out.LeftOff = append(out.LeftOff, item.SyncID)
			continue
		}
		out.Profiles = append(out.Profiles, ProfileSetMember{SyncID: item.SyncID, ID: row.ID})
		if row.IsChild && !row.IsPrimary && row.LibraryRestrictionsEnabled && len(row.AllowedLibraryIDs) == 0 {
			out.AwaitingLibraries = append(out.AwaitingLibraries, item.SyncID)
		}
		if _, held, _ := heldPicture(&row.Profile, item.Picture); !held {
			out.AwaitingPictures = append(out.AwaitingPictures, item.SyncID)
		}
		if row.PINHash == "" && profileSetAwaitingPin(row.SyncApplied) {
			out.AwaitingPins = append(out.AwaitingPins, item.SyncID)
		}
		for _, key := range settings[i].skipped {
			out.SkippedSettings = append(out.SkippedSettings, ProfileSetSkippedSetting{SyncID: item.SyncID, Key: key})
		}
	}
	return out
}

// sameHash compares two content hashes.
func sameHash(a, b []byte) bool { return bytes.Equal(a, b) }
