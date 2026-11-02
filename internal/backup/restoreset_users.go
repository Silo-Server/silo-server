package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/models"
)

// UserDocument is users/<account id>.json: one account, its profiles with
// their chosen data, and the items that data references.
type UserDocument struct {
	Account  AccountEntry   `json:"account"`
	Profiles []ProfileEntry `json:"profiles"`
	// Items describe every content id the profiles' data references, with
	// provider ids, so another server can match what its catalog lacks.
	Items []ItemRef `json:"items"`
}

// AccountEntry is one users row: identity, login, and every policy override
// the access resolver reads. The owner and break-glass flags are not
// carried: a restore never sets them.
type AccountEntry struct {
	ID                        int      `json:"id"`
	Username                  string   `json:"username"`
	Email                     string   `json:"email"`
	PasswordHash              string   `json:"password_hash"`
	LocalPasswordLoginEnabled bool     `json:"local_password_login_enabled"`
	PasswordChangeRequired    bool     `json:"password_change_required"`
	Enabled                   bool     `json:"enabled"`
	Role                      string   `json:"role"`
	Permissions               []string `json:"permissions"`
	// Policy holds the account's overrides; a null field inherits.
	Policy AccountPolicy `json:"policy"`
	// Group is the account's access group in the backup, nil when it had
	// none.
	Group *GroupEntry `json:"group"`
	// Effective is the account's resolved policy in the backup, against its
	// group, with the backup's library ids.
	Effective EffectivePolicy `json:"effective_policy"`
}

// AccountPolicy is an account's policy overrides. LibraryIDs null inherits
// and an empty list grants no library.
type AccountPolicy struct {
	LibraryIDs                 []int   `json:"library_ids"`
	MaxPlaybackQuality         *string `json:"max_playback_quality"`
	MaxStreams                 *int    `json:"max_streams"`
	MaxTranscodes              *int    `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps *int    `json:"max_remote_stream_bitrate_kbps"`
	MaxLocalStreamBitrateKbps  *int    `json:"max_local_stream_bitrate_kbps"`
	TranscodeAllowed           *bool   `json:"transcode_allowed"`
	AudioTranscodeAllowed      *bool   `json:"audio_transcode_allowed"`
	MaxProfiles                *int    `json:"max_profiles"`
	DownloadAllowed            *bool   `json:"download_allowed"`
	DownloadTranscodeAllowed   *bool   `json:"download_transcode_allowed"`
	RequestsAllowed            *bool   `json:"requests_allowed"`
}

// GroupEntry is an access group's name and policy in the backup.
type GroupEntry struct {
	Name   string      `json:"name"`
	Policy GroupPolicy `json:"policy"`
}

// GroupPolicy is access.GroupPolicy as a restore set carries it.
// LibraryIDs and AllowedPermissions null mean unrestricted.
type GroupPolicy struct {
	LibraryIDs                 []int    `json:"library_ids"`
	MaxPlaybackQuality         string   `json:"max_playback_quality"`
	DownloadAllowed            bool     `json:"download_allowed"`
	DownloadTranscodeAllowed   bool     `json:"download_transcode_allowed"`
	TranscodeAllowed           bool     `json:"transcode_allowed"`
	AudioTranscodeAllowed      bool     `json:"audio_transcode_allowed"`
	MaxStreams                 int      `json:"max_streams"`
	MaxTranscodes              int      `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps int      `json:"max_remote_stream_bitrate_kbps"`
	MaxLocalStreamBitrateKbps  int      `json:"max_local_stream_bitrate_kbps"`
	MaxProfiles                int      `json:"max_profiles"`
	AllowedPermissions         []string `json:"allowed_permissions"`
	RequestsAllowed            bool     `json:"requests_allowed"`
}

// EffectivePolicy is access.EffectiveUserPolicy as a restore set carries it.
// LibraryIDs null is unrestricted; 0 caps are unlimited.
type EffectivePolicy struct {
	LibraryIDs                 []int    `json:"library_ids"`
	MaxPlaybackQuality         string   `json:"max_playback_quality"`
	DownloadAllowed            bool     `json:"download_allowed"`
	DownloadTranscodeAllowed   bool     `json:"download_transcode_allowed"`
	TranscodeAllowed           bool     `json:"transcode_allowed"`
	AudioTranscodeAllowed      bool     `json:"audio_transcode_allowed"`
	MaxStreams                 int      `json:"max_streams"`
	MaxTranscodes              int      `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps int      `json:"max_remote_stream_bitrate_kbps"`
	MaxLocalStreamBitrateKbps  int      `json:"max_local_stream_bitrate_kbps"`
	MaxProfiles                int      `json:"max_profiles"`
	Permissions                []string `json:"permissions"`
	RequestsAllowed            bool     `json:"requests_allowed"`
}

// ProfileEntry is one user_profiles row: name, avatar, primary flag, PIN
// hash, and every field the access resolver reads, with the profile's data
// by data type.
type ProfileEntry struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Avatar                     string `json:"avatar"`
	IsPrimary                  bool   `json:"is_primary"`
	PINHash                    string `json:"pin_hash"`
	IsChild                    bool   `json:"is_child"`
	MaxContentRating           string `json:"max_content_rating"`
	MaxAdvisoryAge             int    `json:"max_advisory_age"`
	RequireAdvisoryAge         bool   `json:"require_advisory_age"`
	LibraryRestrictionsEnabled bool   `json:"library_restrictions_enabled"`
	AllowedLibraryIDs          []int  `json:"allowed_library_ids"`
	MaxPlaybackQuality         string `json:"max_playback_quality"`
	// Data holds each data type's document by data type id.
	Data map[string]json.RawMessage `json:"data"`
	// counts is each data type's entry count, recorded when the reader
	// validated Data, so a preview does not decode the documents again.
	counts map[string]int
}

// ItemRef describes one catalog item a profile's data names.
type ItemRef struct {
	ContentID string `json:"content_id"`
	// Kind is movie, series, episode, or the item's own type; empty when
	// the item was not in the catalog when the backup was taken.
	Kind    string     `json:"kind"`
	Title   string     `json:"title"`
	Year    int        `json:"year"`
	IMDbID  string     `json:"imdb_id"`
	TMDBID  string     `json:"tmdb_id"`
	TVDBID  string     `json:"tvdb_id"`
	Series  *SeriesRef `json:"series"`
	Season  int        `json:"season"`
	Episode int        `json:"episode"`
}

// SeriesRef is an episode's series.
type SeriesRef struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	IMDbID    string `json:"imdb_id"`
	TMDBID    string `json:"tmdb_id"`
	TVDBID    string `json:"tvdb_id"`
}

// label is how reports name an item.
func (r ItemRef) label() string {
	title := strings.TrimSpace(r.Title)
	if r.Series != nil && r.Kind == historyimport.KindEpisode {
		series := strings.TrimSpace(r.Series.Title)
		if series != "" {
			title = fmt.Sprintf("%s S%02dE%02d", series, r.Season, r.Episode)
		}
	}
	if title == "" {
		return r.ContentID
	}
	if r.Year > 0 && r.Kind != "episode" {
		return fmt.Sprintf("%s (%d)", title, r.Year)
	}
	return title
}

const uploadedAvatarPrefix = "upload:"

// avatarBlob is the blob key an uploaded avatar names, or "".
func avatarBlob(avatar string) string {
	key, ok := strings.CutPrefix(avatar, uploadedAvatarPrefix)
	if !ok || !strings.HasPrefix(key, "profile-avatars/") || strings.Contains(key, "://") {
		return ""
	}
	return key
}

// ---- Writing ----

// addUsers writes one document per account, read from the snapshot. An
// account the restore set cannot carry (its document would exceed the
// reader's limit, or reading it failed) is left out and recorded in the
// index's omitted list, which the manifest, the backup list, and the preview
// show; the dump still holds it.
func (b *restoreSetBuild) addUsers(ctx context.Context, tx pgx.Tx, spec sectionSpec) error {
	accounts, err := readBackupAccounts(ctx, tx)
	if err != nil {
		return err
	}
	si := SectionIndex{ID: spec.id, Files: []FileDigest{}, Blobs: []string{}}
	for _, account := range accounts {
		data, blobs, err := exportUserIsolated(ctx, tx, account)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			slog.WarnContext(ctx, "an account could not be read into the restore set; the dump still holds it",
				"component", "backup", "account_id", account.ID, "error", err)
			b.omitFailed(spec, account, err)
			continue
		}
		if len(data) > maxUserDocument {
			b.omit(spec, account, OmittedTooLarge, fmt.Sprintf("Its data is %d bytes, more than a restore set document may hold (%d).", len(data), maxUserDocument))
			continue
		}
		name := spec.itemDir + strconv.Itoa(account.ID) + spec.itemExt
		si.Files = append(si.Files, b.addFile(name, data))
		si.Blobs, b.blobs = appendSectionBlobs(spec, si.Blobs, b.blobs, blobs)
		si.Count++
	}
	b.index.Sections = append(b.index.Sections, si)
	return nil
}

func (b *restoreSetBuild) omit(spec sectionSpec, account AccountEntry, reason, message string) {
	label := account.Username
	if label == "" {
		label = account.Email
	}
	b.index.Omitted = append(b.index.Omitted, OmittedItem{Section: spec.id, ID: strconv.Itoa(account.ID), Label: label, Reason: reason, Message: message})
}

// omitFailed records an account whose export failed, with what failed and
// the underlying error as separate fields a client can group by.
func (b *restoreSetBuild) omitFailed(spec sectionSpec, account AccountEntry, err error) {
	b.omit(spec, account, OmittedExportFailed, truncateText("Its data could not be read: "+err.Error(), maxOmittedText))
	last := &b.index.Omitted[len(b.index.Omitted)-1]
	last.Cause, last.Error = exportCause(err), truncateText(rootError(err).Error(), maxOmittedText)
}

// maxOmittedText bounds an omitted item's message, cause, and error.
const maxOmittedText = 4096

// exportError is an export step that failed: the part being read (a data
// type, or "profiles" and "items" around them) and its error.
type exportError struct {
	part    string
	profile string
	err     error
}

func (e *exportError) Error() string {
	if e.profile != "" {
		return fmt.Sprintf("profile %s %s: %s", e.profile, e.part, e.err)
	}
	return e.err.Error()
}

func (e *exportError) Unwrap() error { return e.err }

// exportCause names what failed without ids: the part, and the step its
// error names first ("notifications: reading notification preferences").
func exportCause(err error) string {
	failure, ok := errors.AsType[*exportError](err)
	if !ok {
		return "account"
	}
	cause := failure.part
	if step, _, found := strings.Cut(failure.err.Error(), ": "); found && rootError(failure.err).Error() != failure.err.Error() {
		cause += ": " + step
	}
	return truncateText(cause, 256)
}

// rootError is the innermost error err wraps.
func rootError(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "")
}

// exportUserIsolated reads one account under a savepoint, so a failing
// query leaves the snapshot transaction usable for the next account.
func exportUserIsolated(ctx context.Context, tx pgx.Tx, account AccountEntry) ([]byte, []blobRef, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	doc, blobs, err := exportUser(ctx, sp, account)
	if err != nil {
		_ = sp.Rollback(ctx)
		return nil, nil, err
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	// A document the reader would refuse makes the whole set unreadable, so
	// it is checked as the reader checks it and left out here instead.
	var written UserDocument
	if err := decodeStrict(data, &written); err != nil {
		return nil, nil, err
	}
	if _, err := validateUserDocument(&written); err != nil {
		return nil, nil, &exportError{part: "document", err: fmt.Errorf("the restore set reader would refuse it: %w", err)}
	}
	return data, blobs, nil
}

func readBackupAccounts(ctx context.Context, tx pgx.Tx) ([]AccountEntry, error) {
	rows, err := tx.Query(ctx, `
		SELECT u.id, COALESCE(u.username, ''), COALESCE(u.email, ''), COALESCE(u.password_hash, ''), u.local_password_login_enabled,
			u.password_change_required, u.enabled, u.role, u.permissions, u.library_ids, u.max_playback_quality, u.max_streams,
			u.max_transcodes, u.max_remote_stream_bitrate_kbps, u.max_local_stream_bitrate_kbps, u.transcode_allowed,
			u.audio_transcode_allowed, u.max_profiles, u.download_allowed, u.download_transcode_allowed, u.requests_allowed,
			g.name, g.library_ids, g.max_playback_quality, g.download_allowed, g.download_transcode_allowed, g.transcode_allowed,
			g.audio_transcode_allowed, g.max_streams, g.max_transcodes, g.max_remote_stream_bitrate_kbps, g.max_local_stream_bitrate_kbps,
			g.max_profiles, g.allowed_permissions, g.requests_allowed
		FROM users u LEFT JOIN access_groups g ON g.id = u.access_group_id
		ORDER BY u.id`)
	if err != nil {
		return nil, fmt.Errorf("reading accounts: %w", err)
	}
	defer rows.Close()
	var out []AccountEntry
	for rows.Next() {
		var a AccountEntry
		var p AccountPolicy
		var name, quality *string
		var gLibraries []int
		var gPermissions []string
		var gDownload, gDownloadTranscode, gTranscode, gAudio, gRequests *bool
		var gStreams, gTranscodes, gRemote, gLocal, gProfiles *int
		if err := rows.Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash, &a.LocalPasswordLoginEnabled, &a.PasswordChangeRequired, &a.Enabled,
			&a.Role, &a.Permissions, &p.LibraryIDs, &p.MaxPlaybackQuality, &p.MaxStreams, &p.MaxTranscodes, &p.MaxRemoteStreamBitrateKbps,
			&p.MaxLocalStreamBitrateKbps, &p.TranscodeAllowed, &p.AudioTranscodeAllowed, &p.MaxProfiles, &p.DownloadAllowed,
			&p.DownloadTranscodeAllowed, &p.RequestsAllowed,
			&name, &gLibraries, &quality, &gDownload, &gDownloadTranscode, &gTranscode, &gAudio, &gStreams, &gTranscodes, &gRemote, &gLocal,
			&gProfiles, &gPermissions, &gRequests); err != nil {
			return nil, err
		}
		if a.Permissions == nil {
			a.Permissions = []string{}
		}
		a.Policy = p
		user := a.user()
		var group *access.GroupPolicy
		if name != nil {
			g := GroupPolicy{LibraryIDs: gLibraries, MaxPlaybackQuality: deref(quality), DownloadAllowed: deref(gDownload),
				DownloadTranscodeAllowed: deref(gDownloadTranscode), TranscodeAllowed: deref(gTranscode), AudioTranscodeAllowed: deref(gAudio),
				MaxStreams: deref(gStreams), MaxTranscodes: deref(gTranscodes), MaxRemoteStreamBitrateKbps: deref(gRemote),
				MaxLocalStreamBitrateKbps: deref(gLocal), MaxProfiles: deref(gProfiles), AllowedPermissions: gPermissions, RequestsAllowed: deref(gRequests)}
			if a.Role != models.RoleAdmin {
				a.Group = &GroupEntry{Name: *name, Policy: g}
				gp := g.access()
				group = &gp
			}
		}
		a.Effective = effectivePolicyOf(access.ApplyGroupPolicy(user, group))
		out = append(out, a)
	}
	return out, rows.Err()
}

func deref[T any](v *T) T {
	var zero T
	if v == nil {
		return zero
	}
	return *v
}

// user is the account as the access resolver reads it. Library ids are as
// in the entry; a caller remaps them first when it resolves here.
func (a AccountEntry) user() *models.User {
	p := a.Policy
	return &models.User{ID: a.ID, Role: a.Role, Permissions: a.Permissions, LibraryIDs: p.LibraryIDs, MaxPlaybackQuality: p.MaxPlaybackQuality,
		MaxStreams: p.MaxStreams, MaxTranscodes: p.MaxTranscodes, MaxRemoteStreamBitrateKbps: p.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps: p.MaxLocalStreamBitrateKbps, TranscodeAllowed: p.TranscodeAllowed, AudioTranscodeAllowed: p.AudioTranscodeAllowed,
		MaxProfiles: p.MaxProfiles, DownloadAllowed: p.DownloadAllowed, DownloadTranscodeAllowed: p.DownloadTranscodeAllowed,
		RequestsAllowed: p.RequestsAllowed}
}

func (g GroupPolicy) access() access.GroupPolicy {
	return access.GroupPolicy{LibraryIDs: g.LibraryIDs, MaxPlaybackQuality: g.MaxPlaybackQuality, DownloadAllowed: g.DownloadAllowed,
		DownloadTranscodeAllowed: g.DownloadTranscodeAllowed, TranscodeAllowed: g.TranscodeAllowed, AudioTranscodeAllowed: g.AudioTranscodeAllowed,
		MaxStreams: g.MaxStreams, MaxTranscodes: g.MaxTranscodes, MaxRemoteStreamBitrateKbps: g.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps: g.MaxLocalStreamBitrateKbps, MaxProfiles: g.MaxProfiles, AllowedPermissions: g.AllowedPermissions,
		RequestsAllowed: g.RequestsAllowed}
}

func effectivePolicyOf(p access.EffectiveUserPolicy) EffectivePolicy {
	return EffectivePolicy{LibraryIDs: p.LibraryIDs, MaxPlaybackQuality: p.MaxPlaybackQuality, DownloadAllowed: p.DownloadAllowed,
		DownloadTranscodeAllowed: p.DownloadTranscodeAllowed, TranscodeAllowed: p.TranscodeAllowed, AudioTranscodeAllowed: p.AudioTranscodeAllowed,
		MaxStreams: p.MaxStreams, MaxTranscodes: p.MaxTranscodes, MaxRemoteStreamBitrateKbps: p.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps: p.MaxLocalStreamBitrateKbps, MaxProfiles: p.MaxProfiles, Permissions: p.Permissions, RequestsAllowed: p.RequestsAllowed}
}

func (p EffectivePolicy) access() access.EffectiveUserPolicy {
	return access.EffectiveUserPolicy{LibraryIDs: p.LibraryIDs, MaxPlaybackQuality: p.MaxPlaybackQuality, DownloadAllowed: p.DownloadAllowed,
		DownloadTranscodeAllowed: p.DownloadTranscodeAllowed, TranscodeAllowed: p.TranscodeAllowed, AudioTranscodeAllowed: p.AudioTranscodeAllowed,
		MaxStreams: p.MaxStreams, MaxTranscodes: p.MaxTranscodes, MaxRemoteStreamBitrateKbps: p.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps: p.MaxLocalStreamBitrateKbps, MaxProfiles: p.MaxProfiles, Permissions: p.Permissions, RequestsAllowed: p.RequestsAllowed}
}

// exportUser reads one account's profiles and every data type of each.
func exportUser(ctx context.Context, tx pgx.Tx, account AccountEntry) (UserDocument, []blobRef, error) {
	doc := UserDocument{Account: account, Profiles: []ProfileEntry{}, Items: []ItemRef{}}
	profiles, err := readBackupProfiles(ctx, tx, account.ID)
	if err != nil {
		return doc, nil, &exportError{part: "profiles", err: err}
	}
	var blobs []blobRef
	var items []string
	for _, p := range profiles {
		if key := avatarBlob(p.Avatar); key != "" {
			blobs = append(blobs, blobRef{store: storeOperational, key: key, ladder: true, source: "profile avatar"})
		}
		p.Data = map[string]json.RawMessage{}
		for _, t := range userDataTypes {
			exp, err := t.Export(ctx, tx, userdata.Owner{UserID: account.ID, ProfileID: p.ID}, nil)
			if err != nil {
				return doc, nil, &exportError{part: t.ID(), profile: p.ID, err: err}
			}
			p.Data[t.ID()] = exp.Data
			items = append(items, exp.Items...)
			for _, b := range exp.Blobs {
				store := storeAssets
				if b.Store == userdata.StoreOperational {
					store = storeOperational
				}
				blobs = append(blobs, blobRef{store: store, key: b.Key, ladder: true, source: t.ID()})
			}
		}
		doc.Profiles = append(doc.Profiles, p)
	}
	if doc.Items, err = readItemRefs(ctx, tx, items); err != nil {
		return doc, nil, &exportError{part: "items", err: err}
	}
	return doc, blobs, nil
}

func readBackupProfiles(ctx context.Context, tx pgx.Tx, userID int) ([]ProfileEntry, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.avatar, p.is_primary, p.pin_hash, p.is_child, p.max_content_rating, COALESCE(p.max_advisory_age, 0),
			p.require_advisory_age, p.library_restrictions_enabled, p.max_playback_quality,
			COALESCE((SELECT array_agg(l.library_id ORDER BY l.library_id) FROM user_profile_allowed_libraries l
				WHERE l.user_id = p.user_id AND l.profile_id = p.id), '{}')
		FROM user_profiles p WHERE p.user_id = $1 ORDER BY p.created_at, p.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading profiles: %w", err)
	}
	defer rows.Close()
	var out []ProfileEntry
	for rows.Next() {
		var p ProfileEntry
		if err := rows.Scan(&p.ID, &p.Name, &p.Avatar, &p.IsPrimary, &p.PINHash, &p.IsChild, &p.MaxContentRating, &p.MaxAdvisoryAge,
			&p.RequireAdvisoryAge, &p.LibraryRestrictionsEnabled, &p.MaxPlaybackQuality, &p.AllowedLibraryIDs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// readItemRefs describes each distinct content id: a movie or series, an
// episode with its series, or, for an id the catalog does not hold, the id
// alone.
func readItemRefs(ctx context.Context, q queryer, ids []string) ([]ItemRef, error) {
	list := slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(list) == 0 {
		return []ItemRef{}, nil
	}
	found := map[string]ItemRef{}
	rows, err := q.Query(ctx, `
		SELECT content_id, type, COALESCE(title, ''), COALESCE(year, 0), COALESCE(imdb_id, ''), COALESCE(tmdb_id, ''), COALESCE(tvdb_id, '')
		FROM media_items WHERE content_id = ANY($1)`, list)
	if err != nil {
		return nil, fmt.Errorf("describing items: %w", err)
	}
	for rows.Next() {
		var r ItemRef
		if err := rows.Scan(&r.ContentID, &r.Kind, &r.Title, &r.Year, &r.IMDbID, &r.TMDBID, &r.TVDBID); err != nil {
			rows.Close()
			return nil, err
		}
		found[r.ContentID] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `
		SELECT e.content_id, COALESCE(e.title, ''), COALESCE(e.imdb_id, ''), COALESCE(e.tmdb_id, ''), COALESCE(e.tvdb_id, ''),
			COALESCE(e.season_number, 0), COALESCE(e.episode_number, 0), e.series_id, COALESCE(s.title, ''), COALESCE(s.year, 0),
			COALESCE(s.imdb_id, ''), COALESCE(s.tmdb_id, ''), COALESCE(s.tvdb_id, '')
		FROM episodes e LEFT JOIN media_items s ON s.content_id = e.series_id
		WHERE e.content_id = ANY($1)`, list)
	if err != nil {
		return nil, fmt.Errorf("describing episodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := ItemRef{Kind: historyimport.KindEpisode, Series: &SeriesRef{}}
		if err := rows.Scan(&r.ContentID, &r.Title, &r.IMDbID, &r.TMDBID, &r.TVDBID, &r.Season, &r.Episode, &r.Series.ContentID,
			&r.Series.Title, &r.Series.Year, &r.Series.IMDbID, &r.Series.TMDBID, &r.Series.TVDBID); err != nil {
			return nil, err
		}
		found[r.ContentID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ItemRef, 0, len(list))
	for _, id := range list {
		r, ok := found[id]
		if !ok {
			r = ItemRef{ContentID: id}
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- Reading ----

// maxUserDocument is the largest account document a restore set carries:
// what the reader accepts. A variable so a test can lower it.
var maxUserDocument = maxRestoreSetDocument

// Bounds on a user document's fields; documents are untrusted.
const (
	maxAccountField  = 512
	maxProfilesInDoc = 1000
)

func (p *restoreSetParser) decodeUsers(spec sectionSpec) (int, error) {
	for _, id := range p.itemIDs(spec) {
		name := spec.itemDir + id + spec.itemExt
		var doc UserDocument
		ok, err := p.document(name, &doc)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("%s is missing", name)
		}
		if strconv.Itoa(doc.Account.ID) != id {
			return 0, fmt.Errorf("%s holds account %d", name, doc.Account.ID)
		}
		blobs, err := validateUserDocument(&doc)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		for _, key := range blobs {
			if err := p.requireReferenced(SectionUsers, key); err != nil {
				return 0, err
			}
		}
		p.rs.Users = append(p.rs.Users, doc)
	}
	return len(p.rs.Users), nil
}

// validateUserDocument checks an untrusted user document and returns the
// blob keys it references.
func validateUserDocument(doc *UserDocument) ([]string, error) {
	if err := validateAccountEntry(doc.Account); err != nil {
		return nil, err
	}
	if len(doc.Profiles) > maxProfilesInDoc {
		return nil, fmt.Errorf("account %d has too many profiles", doc.Account.ID)
	}
	var blobs []string
	seen := map[string]bool{}
	for i := range doc.Profiles {
		p := &doc.Profiles[i]
		if !userdata.ValidContentID(p.ID) || seen[p.ID] {
			return nil, fmt.Errorf("profile id %q is not valid or is listed twice", p.ID)
		}
		seen[p.ID] = true
		keys, err := validateProfileEntry(p)
		if err != nil {
			return nil, err
		}
		blobs = append(blobs, keys...)
	}
	if len(doc.Items) > userdata.MaxEntries {
		return nil, fmt.Errorf("account %d lists too many items", doc.Account.ID)
	}
	items := map[string]bool{}
	for _, r := range doc.Items {
		if !userdata.ValidContentID(r.ContentID) || items[r.ContentID] {
			return nil, fmt.Errorf("item %q is not valid or is listed twice", r.ContentID)
		}
		items[r.ContentID] = true
		if len(r.Title) > maxAccountField*4 || len(r.IMDbID) > 64 || len(r.TMDBID) > 64 || len(r.TVDBID) > 64 {
			return nil, fmt.Errorf("item %q is not valid", r.ContentID)
		}
	}
	return blobs, nil
}

func validateAccountEntry(a AccountEntry) error {
	if a.ID <= 0 {
		return fmt.Errorf("account id %d is not valid", a.ID)
	}
	for _, s := range []string{a.Username, a.Email, a.PasswordHash} {
		if len(s) > maxAccountField || !utf8.ValidString(s) {
			return fmt.Errorf("account %d has a field that is too long or not text", a.ID)
		}
	}
	switch {
	case strings.TrimSpace(a.Username) == "" && strings.TrimSpace(a.Email) == "":
		return fmt.Errorf("account %d has neither a username nor an email", a.ID)
	case a.Role != models.RoleAdmin && a.Role != models.RoleUser:
		return fmt.Errorf("account %d has role %q", a.ID, a.Role)
	case len(a.Permissions) > 64:
		return fmt.Errorf("account %d lists too many permissions", a.ID)
	case a.Group != nil && (strings.TrimSpace(a.Group.Name) == "" || len(a.Group.Name) > maxAccountField):
		return fmt.Errorf("account %d names an invalid group", a.ID)
	}
	return nil
}

// count is a data type's entry count: the one the reader recorded, or, for
// a profile no reader validated, the document's own.
func (p *ProfileEntry) count(t userdata.Type, raw json.RawMessage) (int, error) {
	if n, ok := p.counts[t.ID()]; ok {
		return n, nil
	}
	sum, err := t.Validate(raw)
	return sum.Count, err
}

// validateProfileEntry checks one profile and its data, records each data
// type's entry count, and returns the blob keys they reference.
func validateProfileEntry(p *ProfileEntry) ([]string, error) {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > maxAccountField || len(p.PINHash) > maxAccountField || len(p.Avatar) > 2048 ||
		len(p.MaxContentRating) > 64 || len(p.MaxPlaybackQuality) > 64 || p.MaxAdvisoryAge < 0 || p.MaxAdvisoryAge > 21 {
		return nil, fmt.Errorf("profile %q is not valid", p.ID)
	}
	var blobs []string
	if key := avatarBlob(p.Avatar); key != "" {
		blobs = append(blobs, key)
	} else if strings.HasPrefix(p.Avatar, uploadedAvatarPrefix) {
		return nil, fmt.Errorf("profile %q names an avatar outside its store", p.ID)
	}
	for typeID, raw := range p.Data {
		t, ok := userDataType(typeID)
		if !ok {
			return nil, fmt.Errorf("profile %q carries unknown data %q", p.ID, typeID)
		}
		sum, err := t.Validate(raw)
		if err != nil {
			return nil, fmt.Errorf("profile %q %s: %w", p.ID, typeID, err)
		}
		if p.counts == nil {
			p.counts = make(map[string]int, len(p.Data))
		}
		p.counts[typeID] = sum.Count
		for _, b := range sum.Blobs {
			blobs = append(blobs, b.Key)
		}
	}
	return blobs, nil
}
