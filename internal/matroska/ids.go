package matroska

// Element IDs, from the EBML and Matroska specifications.
const (
	IDEBML               = 0x1A45DFA3
	IDEBMLVersion        = 0x4286
	IDEBMLReadVersion    = 0x42F7
	IDEBMLMaxIDLength    = 0x42F2
	IDEBMLMaxSizeLength  = 0x42F3
	IDDocType            = 0x4282
	IDDocTypeVersion     = 0x4287
	IDDocTypeReadVersion = 0x4285
	IDVoid               = 0xEC
	IDCRC32              = 0xBF

	IDSegment      = 0x18538067
	IDSeekHead     = 0x114D9B74
	IDSeek         = 0x4DBB
	IDSeekID       = 0x53AB
	IDSeekPosition = 0x53AC

	IDInfo           = 0x1549A966
	IDTimestampScale = 0x2AD7B1
	IDDuration       = 0x4489
	IDMuxingApp      = 0x4D80
	IDWritingApp     = 0x5741

	IDTracks               = 0x1654AE6B
	IDTrackEntry           = 0xAE
	IDTrackNumber          = 0xD7
	IDTrackUID             = 0x73C5
	IDTrackType            = 0x83
	IDFlagEnabled          = 0xB9
	IDFlagDefault          = 0x88
	IDFlagForced           = 0x55AA
	IDFlagHearingImpaired  = 0x55AB
	IDFlagLacing           = 0x9C
	IDDefaultDuration      = 0x23E383
	IDTrackTimestampScale  = 0x23314F
	IDCodecDelay           = 0x56AA
	IDName                 = 0x536E
	IDLanguage             = 0x22B59C
	IDLanguageBCP47        = 0x22B59D
	IDCodecID              = 0x86
	IDCodecPrivate         = 0x63A2
	IDContentEncodings     = 0x6D80
	IDContentEncoding      = 0x6240
	IDContentEncodingOrder = 0x5031
	IDContentEncodingScope = 0x5032
	IDContentEncodingType  = 0x5033
	IDContentCompression   = 0x5034
	IDContentCompAlgo      = 0x4254
	IDContentCompSettings  = 0x4255
	IDContentEncryption    = 0x5035
	IDCues                 = 0x1C53BB6B
	IDCuePoint             = 0xBB
	IDCueTime              = 0xB3
	IDCueTrackPositions    = 0xB7
	IDCueTrack             = 0xF7
	IDCueClusterPosition   = 0xF1
	IDCueRelativePosition  = 0xF0
	IDCueDuration          = 0xB2
	IDCueBlockNumber       = 0x5378
	IDCueCodecState        = 0xEA
	IDCueReference         = 0xDB
	IDCluster              = 0x1F43B675
	IDClusterTimestamp     = 0xE7
	IDSimpleBlock          = 0xA3
	IDBlockGroup           = 0xA0
	IDBlock                = 0xA1
	IDBlockDuration        = 0x9B
	IDReferenceBlock       = 0xFB
	IDBlockAdditions       = 0x75A1
	IDBlockMore            = 0xA6
	IDBlockAddID           = 0xEE
	IDBlockAdditional      = 0xA5
	IDTags                 = 0x1254C367
	IDTag                  = 0x7373
	IDTargets              = 0x63C0
	IDTagTrackUID          = 0x63C5
	IDSimpleTag            = 0x67C8
	IDTagName              = 0x45A3
	IDTagString            = 0x4487
	IDChapters             = 0x1043A770
	IDAttachments          = 0x1941A469
)

// Track types, from the Matroska specification.
const (
	TrackTypeVideo    = 1
	TrackTypeAudio    = 2
	TrackTypeSubtitle = 17
	TrackTypeMetadata = 33
)

// DefaultTimestampScale is the Info TimestampScale a file that omits it
// uses: nanoseconds per timestamp tick.
const DefaultTimestampScale = 1_000_000
