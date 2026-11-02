package playback

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Signed HLS playlists repeat the same access and reconstruction query on
// every segment URI. The signed stream token alone is about 1 KB, so a
// two-hour title at two-second segments turns into 4-6 MB of duplicate JWT
// text: slow to transfer and parse in hls.js, and above the 4 MiB a Roku
// player accepts (it refuses the playlist, or silently truncates a chunked
// one and stops refreshing an EVENT playlist past that size). HLS variable
// substitution (EXT-X-DEFINE, protocol version 8) carries the repeated value
// once while every resolved segment URL stays byte-identical. Small playlists
// keep the simpler literal form; below this byte threshold the saving is
// immaterial.
const minManifestQuerySubstitutionSavings = 64 * 1024

// manifestQueryVariable carries a playlist's whole request query.
const manifestQueryVariable = "silo_query"

// manifestParamVariablePrefix names the variable that carries a single query
// parameter value appended after the playlist was built (the relay's "st").
const manifestParamVariablePrefix = "silo_"

// manifestVariableMinVersion is the protocol version that introduced
// EXT-X-DEFINE.
const manifestVariableMinVersion = 8

// maxManifestQueryBytes bounds the request query a playlist repeats on its
// URIs. A client without EXT-X-DEFINE gets the query on every URI of FFmpeg's
// real playlist, up to maxSyntheticManifestSegments of them, so an unbounded
// query multiplies into gigabytes. The largest real query, a stream token
// carrying a PATH_MAX media path plus an account token, is about 7 KB.
const maxManifestQueryBytes = 16 * 1024

// manifestQueryEscaper percent-encodes the characters an EXT-X-DEFINE value
// (a quoted-string) cannot hold, so substitution applies to any query. Real
// queries never contain them, and the encoding keeps the decoded parameters.
var manifestQueryEscaper = strings.NewReplacer(`"`, "%22", "\r", "%0D", "\n", "%0A")

// manifestQuery returns rawQuery in the form a playlist's URIs repeat, or
// ErrManifestQueryTooLong. The bound applies to the escaped form, the one
// repeated, since escaping triples each quote.
func manifestQuery(rawQuery string) (string, error) {
	if len(rawQuery) > maxManifestQueryBytes {
		return "", ErrManifestQueryTooLong
	}
	query := manifestQueryEscaper.Replace(rawQuery)
	if len(query) > maxManifestQueryBytes {
		return "", ErrManifestQueryTooLong
	}
	return query, nil
}

// minRewrittenURISavings is the per-URI saving below which a playlist the
// server rewrites (FFmpeg's real playlist, a relayed node playlist) keeps the
// literal form. Only a credential-sized value (a signed stream token or an
// account token, hundreds of bytes) is worth substituting there; short
// queries such as the Jellyfin-compatibility source_timeline flag stay
// literal for clients that may not support EXT-X-DEFINE. The decision depends
// on the value alone, never on how many segments the playlist lists yet, so a
// growing EVENT playlist keeps one form from its first fetch to its last.
const minRewrittenURISavings = 64

// PlaylistSizeWarnBytes is the rendered-playlist size above which a served
// HLS playlist is logged as oversized. Roku refuses a media playlist larger
// than 4 MiB; warning at 3.5 MiB leaves room for an EVENT playlist that is
// still growing.
const PlaylistSizeWarnBytes = 3584 * 1024

// manifestVariable returns the EXT-X-DEFINE tag (without newline) and the
// reference that replace value in URIs, or empty strings when the literal
// form should stay: each URI saves less than minSavingPerURI bytes, or value
// cannot be carried in a quoted-string attribute.
func manifestVariable(name, value string, minSavingPerURI int) (definition, reference string) {
	reference = "{$" + name + "}"
	saving := len(value) - len(reference)
	if saving <= 0 || saving < minSavingPerURI || strings.ContainsAny(value, "\"\r\n") {
		return "", ""
	}
	return fmt.Sprintf("#EXT-X-DEFINE:NAME=\"%s\",VALUE=\"%s\"", name, value), reference
}

// syntheticManifestQuery returns the header line, URI suffix, and minimum HLS
// version for a playlist the server builds whose segmentCount URIs all end in
// ?rawQuery.
func syntheticManifestQuery(segmentCount int, rawQuery string) (definition, suffix string, minVersion int) {
	if rawQuery == "" {
		return "", "", 0
	}
	definition, reference := manifestVariable(manifestQueryVariable, rawQuery, 1)
	if definition == "" || int64(segmentCount)*int64(len(rawQuery)-len(reference)) < minManifestQuerySubstitutionSavings {
		return "", "?" + rawQuery, 0
	}
	return definition + "\n", "?" + reference, manifestVariableMinVersion
}

// isMediaPlaylist reports whether lines form a media playlist rather than a
// multivariant playlist. EXT-X-TARGETDURATION is required in every media
// playlist, including a growing EVENT playlist that lists no segment yet, and
// is not allowed in a multivariant playlist.
func isMediaPlaylist(lines [][]byte) bool {
	for _, line := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("#EXT-X-TARGETDURATION:")) {
			return true
		}
	}
	return false
}

// insertManifestDefinition adds definition after the playlist's version tag,
// raising the version to one that allows EXT-X-DEFINE, or adds both after
// #EXTM3U when the playlist declares no version. The caller has validated the
// #EXTM3U header.
func insertManifestDefinition(lines [][]byte, definition string) [][]byte {
	insertAt := -1
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if value, ok := bytes.CutPrefix(trimmed, []byte("#EXT-X-VERSION:")); ok {
			if version, err := strconv.Atoi(string(bytes.TrimSpace(value))); err != nil || version < manifestVariableMinVersion {
				lines[i] = []byte(fmt.Sprintf("#EXT-X-VERSION:%d", manifestVariableMinVersion))
			}
			insertAt = i + 1
			break
		}
	}
	added := [][]byte{[]byte(definition)}
	if insertAt < 0 {
		for i, line := range lines {
			if bytes.Equal(bytes.TrimSpace(line), []byte("#EXTM3U")) {
				insertAt = i + 1
				break
			}
		}
		added = [][]byte{[]byte(fmt.Sprintf("#EXT-X-VERSION:%d", manifestVariableMinVersion)), []byte(definition)}
	}
	if insertAt < 0 {
		return lines
	}
	result := make([][]byte, 0, len(lines)+len(added))
	result = append(result, lines[:insertAt]...)
	result = append(result, added...)
	return append(result, lines[insertAt:]...)
}

// isManifestVariableName reports whether name uses only the characters an
// EXT-X-DEFINE NAME allows.
func isManifestVariableName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// WarnIfPlaylistOversized logs a served HLS playlist larger than
// PlaylistSizeWarnBytes. It names the session and sizes only: playlist bodies
// carry signed URLs and must never be logged.
func WarnIfPlaylistOversized(ctx context.Context, component, sessionID string, manifest []byte) {
	if len(manifest) <= PlaylistSizeWarnBytes {
		return
	}
	slog.WarnContext(ctx, "hls playlist exceeds the safe client size",
		"component", component,
		"playback_session_id", sessionID,
		"bytes", len(manifest),
		"limit_bytes", PlaylistSizeWarnBytes,
		"segments", bytes.Count(manifest, []byte("#EXTINF:")),
		"variable_substitution", bytes.Contains(manifest, []byte("#EXT-X-DEFINE:")),
	)
}
