package matroska

// ContentEncodingType values.
const (
	ContentEncodingCompression = 0
	ContentEncodingEncryption  = 1
)

// ContentCompAlgo values.
const (
	CompressionZlib            = 0
	CompressionBzlib           = 1
	CompressionLZO             = 2
	CompressionHeaderStripping = 3
)

// ContentEncodingScope bits.
const (
	ScopeFrames       = 1
	ScopeCodecPrivate = 2
	ScopeNextEncoding = 4
)

// ContentEncoding is one ContentEncoding of a TrackEntry, with the
// specification's defaults for what it omits.
type ContentEncoding struct {
	Order uint64
	Scope uint64
	Type  uint64
	// CompAlgo and CompSettings describe the ContentCompression of a
	// compression encoding.
	CompAlgo     uint64
	CompSettings []byte
}

// MaxContentEncodings is how many ContentEncodings ParseTrackEntry keeps of
// a TrackEntry. Real files chain at most two (compression and encryption),
// and a reader that meets more than one treats the track as unsupported, so
// keeping the first few loses nothing; it bounds what a crafted entry of
// empty encodings can make the parser allocate.
const MaxContentEncodings = 8

// TrackEntry is the part of a TrackEntry the readers use. Fields the entry
// omits take the specification's defaults. Byte slices alias the Tracks data
// they were parsed from.
type TrackEntry struct {
	Number  uint64
	UID     uint64
	Type    uint64
	CodecID string
	// CodecPrivate is as stored, still encoded when a ContentEncoding's
	// scope covers it.
	CodecPrivate  []byte
	Name          string
	Language      string
	LanguageBCP47 string
	Enabled       bool
	Default       bool
	Forced        bool
	// HearingImpaired is FlagHearingImpaired, false when the entry has none.
	HearingImpaired bool
	// DefaultDuration is in nanoseconds, 0 when absent.
	DefaultDuration uint64
	// CodecDelay is subtracted from timestamps, in nanoseconds.
	CodecDelay uint64
	// TimestampScale is the deprecated TrackTimestampScale, 1 when absent.
	TimestampScale float64
	// Encodings holds at most MaxContentEncodings entries.
	Encodings []ContentEncoding
}

// ParseTrackEntry decodes a TrackEntry's data.
func ParseTrackEntry(entry []byte) TrackEntry {
	t := TrackEntry{Language: "eng", Enabled: true, Default: true, TimestampScale: 1}
	Children(entry, func(id uint64, v []byte) {
		switch id {
		case IDTrackNumber:
			t.Number = Uint(v)
		case IDTrackUID:
			t.UID = Uint(v)
		case IDTrackType:
			t.Type = Uint(v)
		case IDCodecID:
			t.CodecID = String(v)
		case IDCodecPrivate:
			t.CodecPrivate = v
		case IDName:
			t.Name = String(v)
		case IDLanguage:
			t.Language = String(v)
		case IDLanguageBCP47:
			t.LanguageBCP47 = String(v)
		case IDFlagEnabled:
			t.Enabled = Uint(v) != 0
		case IDFlagDefault:
			t.Default = Uint(v) != 0
		case IDFlagForced:
			t.Forced = Uint(v) != 0
		case IDFlagHearingImpaired:
			t.HearingImpaired = Uint(v) != 0
		case IDDefaultDuration:
			t.DefaultDuration = Uint(v)
		case IDCodecDelay:
			t.CodecDelay = Uint(v)
		case IDTrackTimestampScale:
			t.TimestampScale = Float(v)
		case IDContentEncodings:
			Children(v, func(id uint64, v []byte) {
				if id == IDContentEncoding && len(t.Encodings) < MaxContentEncodings {
					t.Encodings = append(t.Encodings, parseContentEncoding(v))
				}
			})
		}
	})
	return t
}

func parseContentEncoding(data []byte) ContentEncoding {
	enc := ContentEncoding{Scope: ScopeFrames}
	Children(data, func(id uint64, v []byte) {
		switch id {
		case IDContentEncodingOrder:
			enc.Order = Uint(v)
		case IDContentEncodingScope:
			enc.Scope = Uint(v)
		case IDContentEncodingType:
			enc.Type = Uint(v)
		case IDContentCompression:
			Children(v, func(id uint64, v []byte) {
				switch id {
				case IDContentCompAlgo:
					enc.CompAlgo = Uint(v)
				case IDContentCompSettings:
					enc.CompSettings = v
				}
			})
		}
	})
	return enc
}
