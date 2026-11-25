package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/procgroup"
)

// Choosing the fonts of a bundle that cannot hold every attachment.
//
// Fansub releases attach a hundred fonts or more, often in alphabetical
// order, so the fonts a script names can come after the bundle's limits. The
// bundle then takes the attachments whose family the script names first: the
// Fontname of each style in the track's header, read from the source's index,
// and the \fn overrides in its events when the subtitle cache holds the
// extracted track. The events are not read from the source here: a whole
// track read through the index admits one reader at a time, and taking it
// would push the player's own read of the script onto a demux of the whole
// file. libass finds a font by the names inside it, not its file name, so
// each attachment's names are read from its name table.

// scriptFontsTimeout bounds reading the track header from the source's index.
const scriptFontsTimeout = 5 * time.Second

// subtitleScriptFonts returns the font families the ASS/SSA track at ordinal
// names, lowercased, or nil when they cannot be read. cache may be nil.
func subtitleScriptFonts(ctx context.Context, inputPath string, ordinal int, cache *SubtitleCache) []string {
	var texts [][]byte
	if cache != nil {
		if script, ok := cache.LookupText(inputPath, ordinal, subtitleFormatASS); ok {
			texts = append(texts, script)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, scriptFontsTimeout)
	defer cancel()
	idx, err := SubtitleIndexes().Index(ctx, inputPath)
	switch {
	case err != nil:
		slog.DebugContext(ctx, "subtitle script fonts: index unavailable", "component", "playback", "error", err)
	// In a Matroska index a track's ordinal is FFmpeg's s:N (see
	// stageBurnInSubtitle); an MP4 index can leave tracks out.
	case idx.Container == embeddedsub.ContainerMatroska && ordinal >= 0 && ordinal < len(idx.Tracks) && IsASS(idx.Tracks[ordinal].FFmpegCodec()):
		texts = append(texts, idx.Tracks[ordinal].CodecPrivate)
	}
	return assScriptFontNames(texts...)
}

// maxScriptFontNames bounds the families read from a script, so a crafted
// track that names a new font in every event cannot keep bundle selection
// busy. A bundle holds 32 fonts, and real scripts name a few dozen families.
const maxScriptFontNames = 256

// assScriptFontNames returns the font families ASS script text names, each
// once, lowercased as libass compares them, in the order first named: the
// Fontname of every style in [V4+ Styles] or [V4 Styles], then the \fn
// override tags of the events, up to maxScriptFontNames. A leading @
// (vertical text) is not part of the family.
func assScriptFontNames(texts ...[]byte) []string {
	var names []string
	seen := make(map[string]bool)
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@")))
		if name != "" && len(names) < maxScriptFontNames && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, text := range texts {
		inStyles := false
		fontField, fields := 1, 0
		for line := range strings.Lines(string(text)) {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section := strings.ToLower(line)
				inStyles = section == "[v4+ styles]" || section == "[v4 styles]"
				fontField, fields = 1, 0
				continue
			}
			if !inStyles {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "format":
				columns := strings.Split(value, ",")
				fields = len(columns)
				for i, column := range columns {
					if strings.EqualFold(strings.TrimSpace(column), "fontname") {
						fontField = i
					}
				}
			case "style":
				n := fields
				if n <= fontField {
					n = -1
				}
				if values := strings.SplitN(value, ",", n); fontField < len(values) {
					add(values[fontField])
				}
			}
		}
	}
	for _, text := range texts {
		for rest := text; ; {
			open := bytes.IndexByte(rest, '{')
			if open < 0 {
				break
			}
			rest = rest[open+1:]
			end := bytes.IndexByte(rest, '}')
			if end < 0 {
				end = len(rest)
			}
			for _, tag := range bytes.Split(rest[:end], []byte{'\\'})[1:] {
				if name, ok := bytes.CutPrefix(tag, []byte("fn")); ok {
					// libass also takes the name in parentheses: \fn(Name).
					name = bytes.TrimSpace(name)
					if len(name) > 1 && name[0] == '(' && name[len(name)-1] == ')' {
						name = name[1 : len(name)-1]
					}
					add(string(name))
				}
			}
			rest = rest[end:]
		}
	}
	return names
}

// scriptFontStreamsFirst reorders streams so the attachments whose family is
// one of families come first, each group in container order, and says how
// many came first. It reads every attachment that could go in a bundle, one
// at a time from one FFmpeg run, to read the names inside it.
func scriptFontStreamsFirst(ctx context.Context, inputPath, ffmpegPath string, streams []attachmentProbeStream, videoStream int, families []string) ([]attachmentProbeStream, int, error) {
	candidates := make([]attachmentProbeStream, 0, len(streams))
	var total int64
	for _, stream := range streams {
		if stream.ExtraDataSize <= 0 || stream.ExtraDataSize > maxSubtitleFontBytes {
			continue
		}
		candidates = append(candidates, stream)
		total += stream.ExtraDataSize
	}
	// The limits of staging every font for burn-in bound what is read here.
	if len(candidates) > maxStagedSubtitleFonts || total > maxStagedSubtitleFontBytes {
		return nil, 0, errStagedSubtitleFontsTooLarge
	}
	named := make(map[string]bool, len(families))
	for _, family := range families {
		named[strings.ToLower(family)] = true
	}
	wanted := map[int]bool{}
	err := readFontAttachments(ctx, inputPath, ffmpegPath, candidates, videoStream, func(stream attachmentProbeStream, data []byte) {
		wanted[stream.Index] = slices.ContainsFunc(fontNames(data), func(name string) bool {
			return named[strings.ToLower(name)]
		})
	})
	if err != nil {
		return nil, 0, err
	}
	ordered := make([]attachmentProbeStream, 0, len(streams))
	for _, stream := range streams {
		if wanted[stream.Index] {
			ordered = append(ordered, stream)
		}
	}
	n := len(ordered)
	for _, stream := range streams {
		if !wanted[stream.Index] {
			ordered = append(ordered, stream)
		}
	}
	return ordered, n, nil
}

// readFontAttachments hands each of streams' attachments to fn in turn, in
// one FFmpeg run that reads only the container's header, holding one font in
// memory at a time. Probed extradata sizes frame each font; output that is
// shorter or longer than they say is an error. data is reused after fn
// returns.
func readFontAttachments(ctx context.Context, inputPath, ffmpegPath string, streams []attachmentProbeStream, videoStream int, fn func(attachmentProbeStream, []byte)) error {
	if len(streams) == 0 {
		return nil
	}
	var largest int64
	for _, stream := range streams {
		largest = max(largest, stream.ExtraDataSize)
	}
	cmd := fspath.CommandContext(ctx, ffmpegPath, fontPipeDumpArgs(inputPath, streams, videoStream)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("subtitle fonts: start attachment read: %w", err)
	}
	buf := make([]byte, largest)
	readErr := func() error {
		for _, stream := range streams {
			data := buf[:stream.ExtraDataSize]
			if _, err := io.ReadFull(stdout, data); err != nil {
				return errors.New("subtitle fonts: attachment data differs from probed size")
			}
			fn(stream, data)
		}
		if n, _ := stdout.Read(buf[:1]); n > 0 {
			return errors.New("subtitle fonts: attachment data exceeds probed size")
		}
		return nil
	}()
	if readErr != nil {
		_ = procgroup.Kill(cmd)
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return readErr
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("subtitle fonts: read attachments: %w", err)
	}
	return nil
}

// Name IDs libass matches a font request against: the family, the full name
// and the PostScript name, plus the typographic family that groups more than
// four styles.
const (
	fontNameIDFamily            = 1
	fontNameIDFull              = 4
	fontNameIDPostScript        = 6
	fontNameIDTypographicFamily = 16
)

// A font's names are read within a fixed budget, so a crafted attachment
// cannot keep bundle selection busy: each distinct name table is read once,
// every name record examined counts toward maxFontNameRecords for the whole
// attachment, collection included, a name stored in more than
// maxFontNameBytes is skipped, and at most maxFontNames distinct names are
// kept. Real fonts, CJK collections included, carry a few hundred records
// and a few dozen short names.
const (
	maxFontNames       = 512
	maxFontNameBytes   = 256
	maxFontNameRecords = 8192
)

// fontNameReader collects an attachment's names within the budget above.
type fontNameReader struct {
	names   []string
	records int
	tables  map[[2]int]bool
}

// fontNames returns the names a TrueType or OpenType font, or each font of a
// collection, gives itself in its name table, in every language it carries,
// within the budget above. It returns nil for data it cannot read, such as
// WOFF.
func fontNames(data []byte) []string {
	if len(data) < 12 {
		return nil
	}
	r := fontNameReader{tables: make(map[[2]int]bool)}
	if string(data[:4]) != "ttcf" {
		r.sfnt(data, 0)
		return r.names
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	for i := range min(count, 256) {
		at := 12 + 4*i
		if at+4 > len(data) || r.spent() {
			break
		}
		r.sfnt(data, int(binary.BigEndian.Uint32(data[at:at+4])))
	}
	return r.names
}

func (r *fontNameReader) spent() bool {
	return r.records >= maxFontNameRecords || len(r.names) >= maxFontNames
}

// sfnt reads the name table of the font whose table directory is at offset
// in data, unless an earlier font of the collection shares it.
func (r *fontNameReader) sfnt(data []byte, offset int) {
	if offset < 0 || offset+12 > len(data) {
		return
	}
	tables := int(binary.BigEndian.Uint16(data[offset+4:]))
	for i := range tables {
		rec := offset + 12 + 16*i
		if rec+16 > len(data) {
			return
		}
		if string(data[rec:rec+4]) != "name" {
			continue
		}
		start := int(binary.BigEndian.Uint32(data[rec+8:]))
		length := int(binary.BigEndian.Uint32(data[rec+12:]))
		if start < 0 || length < 6 || start > len(data) || length > len(data)-start {
			return
		}
		if key := [2]int{start, length}; !r.tables[key] {
			r.tables[key] = true
			r.nameTable(data[start : start+length])
		}
		return
	}
}

// nameTable adds a name table's distinct names, charging every record it
// examines to the budget.
func (r *fontNameReader) nameTable(table []byte) {
	count := int(binary.BigEndian.Uint16(table[2:]))
	storage := int(binary.BigEndian.Uint16(table[4:]))
	for i := range count {
		if r.spent() {
			return
		}
		r.records++
		rec := 6 + 12*i
		if rec+12 > len(table) {
			return
		}
		platform := binary.BigEndian.Uint16(table[rec:])
		encoding := binary.BigEndian.Uint16(table[rec+2:])
		switch binary.BigEndian.Uint16(table[rec+6:]) {
		case fontNameIDFamily, fontNameIDFull, fontNameIDPostScript, fontNameIDTypographicFamily:
		default:
			continue
		}
		length := int(binary.BigEndian.Uint16(table[rec+8:]))
		start := storage + int(binary.BigEndian.Uint16(table[rec+10:]))
		if length > maxFontNameBytes || start+length > len(table) {
			continue
		}
		raw := table[start : start+length]
		var name string
		switch {
		case platform == 0 || platform == 3:
			units := make([]uint16, len(raw)/2)
			for j := range units {
				units[j] = binary.BigEndian.Uint16(raw[2*j:])
			}
			name = string(utf16.Decode(units))
		case platform == 1 && encoding == 0:
			// Mac Roman; family names in it are ASCII in practice.
			runes := make([]rune, len(raw))
			for j, b := range raw {
				runes[j] = rune(b)
			}
			name = string(runes)
		default:
			continue
		}
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(r.names, name) {
			r.names = append(r.names, name)
		}
	}
}
