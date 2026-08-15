package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	SchemaVersion        = 1
	MaxStackExcerptBytes = 8192
	// MaxLogLineBytes bounds a single newline-delimited entry line
	// (logs.jsonl / breadcrumbs.jsonl) during streaming bundle validation. It
	// is well above the per-field caps a log line can legitimately reach (msg
	// 2048, tag/run 128, plus registered attrs) so it never rejects a valid
	// line, while keeping per-line memory bounded when validating.
	MaxLogLineBytes = 64 * 1024
)

var (
	manifestRequiredFields = []string{
		"schema_version",
		"report",
		"destination",
		"consent",
		"device_summary",
		"playback_session_ids",
		"log_summary",
		"archive",
	}
	manifestReportRequiredFields = []string{
		"type",
		"captured_at",
		"capture_session_id",
		"app_version",
		"app_build",
		"platform",
		"os_version",
	}
	manifestDestinationRequiredFields = []string{"server_instance_id"}
	manifestConsentRequiredFields     = []string{"mode", "notice_version"}
	manifestCrashRequiredFields       = []string{"summary", "source", "provenance", "occurred_at"}
	manifestDeviceSummaryFields       = []string{"manufacturer", "model", "os", "form_factor"}
	manifestLogSummaryRequiredFields  = []string{"lines", "bytes_gz", "dropped_lines", "categories", "debug_logging"}
	manifestArchiveRequiredFields     = []string{"entries", "bytes", "uncompressed_bytes", "sha256"}

	reportTypes       = []string{"crash", "anr", "native_crash", "hang", "abnormal_exit", "manual"}
	platforms         = []string{"android", "android-tv", "ios", "tvos"}
	crashSources      = []string{"ueh", "exit_info", "metrickit", "exit_sentinel"}
	crashProvenances  = []string{"pre_failure", "post_restart", "metric_reporting_period"}
	consentModes      = []string{"prompt", "always", "manual"}
	logLevels         = []string{"V", "D", "I", "W", "E"}
	logCategories     = []string{"playback", "focus", "network", "lifecycle", "browse", "cast", "download", "crash", "other"}
	deviceProvenances = []string{
		"pre_failure",
		"post_restart",
	}
	deviceRequiredFields = []string{"captured_at", "provenance", "identity", "display", "audio", "video_codecs", "network"}

	ArchiveEntryAllowlist = []string{
		"manifest.json",
		"device.json",
		"logs.jsonl",
		"crash/summary.json",
		"crash/stack.txt",
		"crash/tombstone.pb",
		"crash/metrickit.json",
		"breadcrumbs.jsonl",
	}

	sha256Pattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
)

type Manifest struct {
	SchemaVersion      int
	Report             Report
	Destination        Destination
	Consent            Consent
	Crash              *Crash
	DeviceSummary      DeviceSummary
	PlaybackSessionIDs []string
	LogSummary         LogSummary
	Archive            Archive
}

type Report struct {
	Type             string
	CapturedAt       time.Time
	CaptureSessionID string
	AppVersion       string
	AppBuild         string
	Platform         string
	OSVersion        string
	ProfileID        string
}

type Destination struct {
	ServerInstanceID string
}

type Consent struct {
	Mode          string
	NoticeVersion int
}

type Crash struct {
	Summary      string
	StackExcerpt string
	Thread       string
	Foreground   *bool
	Source       string
	Provenance   string
	OccurredAt   time.Time
}

type DeviceSummary struct {
	Manufacturer string
	Model        string
	OS           string
	FormFactor   string
}

type LogSummary struct {
	Lines        int64
	BytesGz      int64
	DroppedLines int64
	Categories   []string
	DebugLogging bool
}

type Archive struct {
	Entries []string
	// Bytes is the size of the bundle part as transmitted (the gzip stream).
	Bytes int64
	// UncompressedBytes is the total size of the decompressed tar stream,
	// including headers, end-of-archive blocks, and record padding — the
	// byte count a client observes between its tar writer and gzip writer.
	UncompressedBytes int64
	// SHA256 is the hex digest of the bundle part as transmitted.
	SHA256 string
}

type DeviceSnapshot struct {
	CapturedAt   time.Time
	Provenance   string
	Identity     json.RawMessage
	Display      json.RawMessage
	Audio        json.RawMessage
	VideoCodecs  json.RawMessage
	Network      json.RawMessage
	OriginalJSON json.RawMessage
}

type LogLine struct {
	Timestamp time.Time
	Run       string
	Level     string
	Category  string
	Tag       string
	Message   string
	Attrs     map[string]any
}

type manifestWire struct {
	SchemaVersion      *int               `json:"schema_version"`
	Report             *reportWire        `json:"report"`
	Destination        *destinationWire   `json:"destination"`
	Consent            *consentWire       `json:"consent"`
	Crash              *crashWire         `json:"crash"`
	DeviceSummary      *deviceSummaryWire `json:"device_summary"`
	PlaybackSessionIDs *[]string          `json:"playback_session_ids"`
	LogSummary         *logSummaryWire    `json:"log_summary"`
	Archive            *archiveWire       `json:"archive"`
}

type reportWire struct {
	Type             *string `json:"type"`
	CapturedAt       *string `json:"captured_at"`
	CaptureSessionID *string `json:"capture_session_id"`
	AppVersion       *string `json:"app_version"`
	AppBuild         *string `json:"app_build"`
	Platform         *string `json:"platform"`
	OSVersion        *string `json:"os_version"`
	ProfileID        *string `json:"profile_id"`
}

type destinationWire struct {
	ServerInstanceID *string `json:"server_instance_id"`
}

type consentWire struct {
	Mode          *string `json:"mode"`
	NoticeVersion *int    `json:"notice_version"`
}

type crashWire struct {
	Summary      *string `json:"summary"`
	StackExcerpt *string `json:"stack_excerpt"`
	Thread       *string `json:"thread"`
	Foreground   *bool   `json:"foreground"`
	Source       *string `json:"source"`
	Provenance   *string `json:"provenance"`
	OccurredAt   *string `json:"occurred_at"`
}

type deviceSummaryWire struct {
	Manufacturer *string `json:"manufacturer"`
	Model        *string `json:"model"`
	OS           *string `json:"os"`
	FormFactor   *string `json:"form_factor"`
}

type logSummaryWire struct {
	Lines        *int64    `json:"lines"`
	BytesGz      *int64    `json:"bytes_gz"`
	DroppedLines *int64    `json:"dropped_lines"`
	Categories   *[]string `json:"categories"`
	DebugLogging *bool     `json:"debug_logging"`
}

type archiveWire struct {
	Entries           *[]string `json:"entries"`
	Bytes             *int64    `json:"bytes"`
	UncompressedBytes *int64    `json:"uncompressed_bytes"`
	SHA256            *string   `json:"sha256"`
}

type logLineWire struct {
	Timestamp *string         `json:"ts"`
	Run       *string         `json:"run"`
	Level     *string         `json:"lvl"`
	Category  *string         `json:"cat"`
	Tag       *string         `json:"tag"`
	Message   *string         `json:"msg"`
	Attrs     json.RawMessage `json:"attrs"`
}

type attrValueType string

const (
	attrString  attrValueType = "string"
	attrInteger attrValueType = "integer"
)

var attrRegistry = map[string]map[string]attrValueType{
	"playback": {
		"sink":            attrString,
		"fmt":             attrString,
		"decoder":         attrString,
		"width":           attrInteger,
		"height":          attrInteger,
		"hdr_mode":        attrString,
		"bitrate_kbps":    attrInteger,
		"dropped_frames":  attrInteger,
		"audio_underruns": attrInteger,
		"session_id":      attrString,
		"play_method":     attrString,
		"reason":          attrString,
		"position_ms":     attrInteger,
	},
	"focus": {
		"target": attrString,
		"action": attrString,
	},
	"network": {
		"method":      attrString,
		"path":        attrString,
		"status":      attrInteger,
		"duration_ms": attrInteger,
		"outcome":     attrString,
		"error_code":  attrString,
		"attempt":     attrInteger,
	},
	"lifecycle": {
		"state":       attrString,
		"phase":       attrString,
		"duration_ms": attrInteger,
		"outcome":     attrString,
		"reason":      attrString,
		"launch_type": attrString,
	},
	"crash": {
		"fingerprint": attrString,
		"source":      attrString,
	},
}

func ValidateManifest(data []byte) (Manifest, error) {
	var wire manifestWire
	if err := decodeJSON(data, &wire); err != nil {
		return Manifest{}, err
	}

	if wire.SchemaVersion == nil {
		return Manifest{}, requiredError("manifest.schema_version")
	}
	if *wire.SchemaVersion != SchemaVersion {
		return Manifest{}, fieldError("manifest.schema_version", "unsupported value %d", *wire.SchemaVersion)
	}

	if wire.Report == nil {
		return Manifest{}, requiredError("manifest.report")
	}
	report, err := validateReport(*wire.Report)
	if err != nil {
		return Manifest{}, err
	}

	if wire.Destination == nil {
		return Manifest{}, requiredError("manifest.destination")
	}
	destination, err := validateDestination(*wire.Destination)
	if err != nil {
		return Manifest{}, err
	}

	if wire.Consent == nil {
		return Manifest{}, requiredError("manifest.consent")
	}
	consent, err := validateConsent(*wire.Consent)
	if err != nil {
		return Manifest{}, err
	}

	// Per the spec the crash object is present for every event report type and
	// absent for manual captures. Enforce both directions so an event report
	// can't be accepted with no crash summary/source/occurred_at to diagnose.
	var crash *Crash
	switch {
	case report.Type == "manual":
		if wire.Crash != nil {
			return Manifest{}, fieldError("manifest.crash", "must be absent for manual reports")
		}
	case wire.Crash == nil:
		return Manifest{}, requiredError("manifest.crash")
	default:
		validated, err := validateCrash(*wire.Crash)
		if err != nil {
			return Manifest{}, err
		}
		crash = &validated
	}

	if wire.DeviceSummary == nil {
		return Manifest{}, requiredError("manifest.device_summary")
	}
	deviceSummary, err := validateDeviceSummary(*wire.DeviceSummary)
	if err != nil {
		return Manifest{}, err
	}

	if wire.PlaybackSessionIDs == nil {
		return Manifest{}, requiredError("manifest.playback_session_ids")
	}
	playbackSessionIDs, err := validateStringSlice("manifest.playback_session_ids", *wire.PlaybackSessionIDs, 20, 1, 128, false)
	if err != nil {
		return Manifest{}, err
	}

	if wire.LogSummary == nil {
		return Manifest{}, requiredError("manifest.log_summary")
	}
	logSummary, err := validateLogSummary(*wire.LogSummary)
	if err != nil {
		return Manifest{}, err
	}

	if wire.Archive == nil {
		return Manifest{}, requiredError("manifest.archive")
	}
	archive, err := validateArchive(*wire.Archive)
	if err != nil {
		return Manifest{}, err
	}

	return Manifest{
		SchemaVersion:      *wire.SchemaVersion,
		Report:             report,
		Destination:        destination,
		Consent:            consent,
		Crash:              crash,
		DeviceSummary:      deviceSummary,
		PlaybackSessionIDs: playbackSessionIDs,
		LogSummary:         logSummary,
		Archive:            archive,
	}, nil
}

func ValidateDevice(data []byte) (DeviceSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(data, &raw); err != nil {
		return DeviceSnapshot{}, err
	}

	capturedAt, err := requiredRawDateTime(raw, "captured_at", "device.captured_at")
	if err != nil {
		return DeviceSnapshot{}, err
	}
	provenance, err := requiredRawString(raw, "provenance", "device.provenance", 1, 32)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	if err := validateEnum("device.provenance", provenance, deviceProvenances); err != nil {
		return DeviceSnapshot{}, err
	}

	identity, err := requiredRawSection(raw, "identity", "device.identity", sectionObject)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	display, err := requiredRawSection(raw, "display", "device.display", sectionObject)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	audio, err := requiredRawSection(raw, "audio", "device.audio", sectionObject)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	videoCodecs, err := requiredRawSection(raw, "video_codecs", "device.video_codecs", sectionArray)
	if err != nil {
		return DeviceSnapshot{}, err
	}
	network, err := requiredRawSection(raw, "network", "device.network", sectionObject)
	if err != nil {
		return DeviceSnapshot{}, err
	}

	return DeviceSnapshot{
		CapturedAt:   capturedAt,
		Provenance:   provenance,
		Identity:     append(json.RawMessage(nil), identity...),
		Display:      append(json.RawMessage(nil), display...),
		Audio:        append(json.RawMessage(nil), audio...),
		VideoCodecs:  append(json.RawMessage(nil), videoCodecs...),
		Network:      append(json.RawMessage(nil), network...),
		OriginalJSON: append(json.RawMessage(nil), bytes.TrimSpace(data)...),
	}, nil
}

func ValidateLogLine(data []byte) (LogLine, error) {
	var wire logLineWire
	if err := decodeJSON(data, &wire); err != nil {
		return LogLine{}, err
	}

	timestamp, err := validateDateTime("log.ts", wire.Timestamp)
	if err != nil {
		return LogLine{}, err
	}
	run, err := validateRequiredString("log.run", wire.Run, 1, 128)
	if err != nil {
		return LogLine{}, err
	}
	level, err := validateRequiredString("log.lvl", wire.Level, 1, 1)
	if err != nil {
		return LogLine{}, err
	}
	if err := validateEnum("log.lvl", level, logLevels); err != nil {
		return LogLine{}, err
	}
	category, err := validateRequiredString("log.cat", wire.Category, 1, 32)
	if err != nil {
		return LogLine{}, err
	}
	if err := validateEnum("log.cat", category, logCategories); err != nil {
		return LogLine{}, err
	}
	tag, err := validateRequiredString("log.tag", wire.Tag, 1, 128)
	if err != nil {
		return LogLine{}, err
	}
	msg, err := validateRequiredString("log.msg", wire.Message, 1, 2048)
	if err != nil {
		return LogLine{}, err
	}

	attrs, err := validateAttrs(category, wire.Attrs)
	if err != nil {
		return LogLine{}, err
	}

	return LogLine{
		Timestamp: timestamp,
		Run:       run,
		Level:     level,
		Category:  category,
		Tag:       tag,
		Message:   msg,
		Attrs:     attrs,
	}, nil
}

func ArchiveEntryAllowed(name string) bool {
	return slices.Contains(ArchiveEntryAllowlist, name)
}

func validateReport(w reportWire) (Report, error) {
	reportType, err := validateRequiredString("manifest.report.type", w.Type, 1, 32)
	if err != nil {
		return Report{}, err
	}
	if err := validateEnum("manifest.report.type", reportType, reportTypes); err != nil {
		return Report{}, err
	}
	capturedAt, err := validateDateTime("manifest.report.captured_at", w.CapturedAt)
	if err != nil {
		return Report{}, err
	}
	captureSessionID, err := validateRequiredString("manifest.report.capture_session_id", w.CaptureSessionID, 1, 128)
	if err != nil {
		return Report{}, err
	}
	appVersion, err := validateRequiredString("manifest.report.app_version", w.AppVersion, 1, 64)
	if err != nil {
		return Report{}, err
	}
	appBuild, err := validateRequiredString("manifest.report.app_build", w.AppBuild, 1, 64)
	if err != nil {
		return Report{}, err
	}
	platform, err := validateRequiredString("manifest.report.platform", w.Platform, 1, 32)
	if err != nil {
		return Report{}, err
	}
	if err := validateEnum("manifest.report.platform", platform, platforms); err != nil {
		return Report{}, err
	}
	osVersion, err := validateRequiredString("manifest.report.os_version", w.OSVersion, 1, 128)
	if err != nil {
		return Report{}, err
	}
	profileID, err := validateOptionalString("manifest.report.profile_id", w.ProfileID, 128)
	if err != nil {
		return Report{}, err
	}

	return Report{
		Type:             reportType,
		CapturedAt:       capturedAt,
		CaptureSessionID: captureSessionID,
		AppVersion:       appVersion,
		AppBuild:         appBuild,
		Platform:         platform,
		OSVersion:        osVersion,
		ProfileID:        profileID,
	}, nil
}

func validateDestination(w destinationWire) (Destination, error) {
	serverInstanceID, err := validateRequiredString("manifest.destination.server_instance_id", w.ServerInstanceID, 1, 128)
	if err != nil {
		return Destination{}, err
	}
	return Destination{ServerInstanceID: serverInstanceID}, nil
}

func validateConsent(w consentWire) (Consent, error) {
	mode, err := validateRequiredString("manifest.consent.mode", w.Mode, 1, 32)
	if err != nil {
		return Consent{}, err
	}
	if err := validateEnum("manifest.consent.mode", mode, consentModes); err != nil {
		return Consent{}, err
	}
	if w.NoticeVersion == nil {
		return Consent{}, requiredError("manifest.consent.notice_version")
	}
	if *w.NoticeVersion < 1 {
		return Consent{}, fieldError("manifest.consent.notice_version", "must be >= 1")
	}
	return Consent{Mode: mode, NoticeVersion: *w.NoticeVersion}, nil
}

func validateCrash(w crashWire) (Crash, error) {
	summary, err := validateRequiredString("manifest.crash.summary", w.Summary, 1, MaxStackExcerptBytes)
	if err != nil {
		return Crash{}, err
	}
	stackExcerpt, err := validateOptionalString("manifest.crash.stack_excerpt", w.StackExcerpt, MaxStackExcerptBytes)
	if err != nil {
		return Crash{}, err
	}
	thread, err := validateOptionalString("manifest.crash.thread", w.Thread, 128)
	if err != nil {
		return Crash{}, err
	}
	source, err := validateRequiredString("manifest.crash.source", w.Source, 1, 32)
	if err != nil {
		return Crash{}, err
	}
	if err := validateEnum("manifest.crash.source", source, crashSources); err != nil {
		return Crash{}, err
	}
	provenance, err := validateRequiredString("manifest.crash.provenance", w.Provenance, 1, 32)
	if err != nil {
		return Crash{}, err
	}
	if err := validateEnum("manifest.crash.provenance", provenance, crashProvenances); err != nil {
		return Crash{}, err
	}
	occurredAt, err := validateDateTime("manifest.crash.occurred_at", w.OccurredAt)
	if err != nil {
		return Crash{}, err
	}

	return Crash{
		Summary:      summary,
		StackExcerpt: stackExcerpt,
		Thread:       thread,
		Foreground:   w.Foreground,
		Source:       source,
		Provenance:   provenance,
		OccurredAt:   occurredAt,
	}, nil
}

func validateDeviceSummary(w deviceSummaryWire) (DeviceSummary, error) {
	manufacturer, err := validateRequiredString("manifest.device_summary.manufacturer", w.Manufacturer, 1, 128)
	if err != nil {
		return DeviceSummary{}, err
	}
	model, err := validateRequiredString("manifest.device_summary.model", w.Model, 1, 128)
	if err != nil {
		return DeviceSummary{}, err
	}
	osVersion, err := validateRequiredString("manifest.device_summary.os", w.OS, 1, 128)
	if err != nil {
		return DeviceSummary{}, err
	}
	formFactor, err := validateRequiredString("manifest.device_summary.form_factor", w.FormFactor, 1, 64)
	if err != nil {
		return DeviceSummary{}, err
	}
	return DeviceSummary{
		Manufacturer: manufacturer,
		Model:        model,
		OS:           osVersion,
		FormFactor:   formFactor,
	}, nil
}

func validateLogSummary(w logSummaryWire) (LogSummary, error) {
	lines, err := validateNonNegativeInt64("manifest.log_summary.lines", w.Lines)
	if err != nil {
		return LogSummary{}, err
	}
	bytesGz, err := validateNonNegativeInt64("manifest.log_summary.bytes_gz", w.BytesGz)
	if err != nil {
		return LogSummary{}, err
	}
	droppedLines, err := validateNonNegativeInt64("manifest.log_summary.dropped_lines", w.DroppedLines)
	if err != nil {
		return LogSummary{}, err
	}
	if w.Categories == nil {
		return LogSummary{}, requiredError("manifest.log_summary.categories")
	}
	categories, err := validateStringSlice("manifest.log_summary.categories", *w.Categories, 9, 1, 32, true)
	if err != nil {
		return LogSummary{}, err
	}
	for i, category := range categories {
		if err := validateEnum(fmt.Sprintf("manifest.log_summary.categories[%d]", i), category, logCategories); err != nil {
			return LogSummary{}, err
		}
	}
	if w.DebugLogging == nil {
		return LogSummary{}, requiredError("manifest.log_summary.debug_logging")
	}
	return LogSummary{
		Lines:        lines,
		BytesGz:      bytesGz,
		DroppedLines: droppedLines,
		Categories:   categories,
		DebugLogging: *w.DebugLogging,
	}, nil
}

func validateArchive(w archiveWire) (Archive, error) {
	if w.Entries == nil {
		return Archive{}, requiredError("manifest.archive.entries")
	}
	entries, err := validateStringSlice("manifest.archive.entries", *w.Entries, len(ArchiveEntryAllowlist), 1, 64, true)
	if err != nil {
		return Archive{}, err
	}
	hasManifest := false
	for i, entry := range entries {
		if !ArchiveEntryAllowed(entry) {
			return Archive{}, fieldError(fmt.Sprintf("manifest.archive.entries[%d]", i), "unsupported value %q", entry)
		}
		if entry == "manifest.json" {
			hasManifest = true
		}
	}
	if !hasManifest {
		return Archive{}, fieldError("manifest.archive.entries", "must include manifest.json")
	}
	bytesCount, err := validateNonNegativeInt64("manifest.archive.bytes", w.Bytes)
	if err != nil {
		return Archive{}, err
	}
	uncompressedBytes, err := validateNonNegativeInt64("manifest.archive.uncompressed_bytes", w.UncompressedBytes)
	if err != nil {
		return Archive{}, err
	}
	sha256Value, err := validateRequiredString("manifest.archive.sha256", w.SHA256, 64, 64)
	if err != nil {
		return Archive{}, err
	}
	if !sha256Pattern.MatchString(sha256Value) {
		return Archive{}, fieldError("manifest.archive.sha256", "must be 64 hexadecimal characters")
	}
	return Archive{
		Entries:           entries,
		Bytes:             bytesCount,
		UncompressedBytes: uncompressedBytes,
		SHA256:            strings.ToLower(sha256Value),
	}, nil
}

type sectionKind int

const (
	sectionObject sectionKind = iota
	sectionArray
)

func requiredRawSection(raw map[string]json.RawMessage, key, path string, kind sectionKind) (json.RawMessage, error) {
	value, ok := raw[key]
	if !ok {
		return nil, requiredError(path)
	}
	if isRawNull(value) {
		return nil, fieldError(path, "must not be null")
	}

	if sentinel, ok, err := rawSentinel(value); err != nil {
		return nil, err
	} else if ok {
		if sentinel != "unknown" && sentinel != "not_collected" {
			return nil, fieldError(path, "unsupported sentinel %q", sentinel)
		}
		return append(json.RawMessage(nil), value...), nil
	}

	switch kind {
	case sectionObject:
		if err := validateDeviceObject(path, value, 0); err != nil {
			return nil, err
		}
	case sectionArray:
		if err := validateVideoCodecs(path, value); err != nil {
			return nil, err
		}
	default:
		return nil, fieldError(path, "unsupported section kind")
	}
	return append(json.RawMessage(nil), value...), nil
}

func validateVideoCodecs(path string, raw json.RawMessage) error {
	var items []json.RawMessage
	if err := decodeJSON(raw, &items); err != nil {
		return fieldError(path, "must be an array or unknown/not_collected")
	}
	if len(items) > 128 {
		return fieldError(path, "must contain <= 128 items")
	}
	for i, item := range items {
		if err := validateDeviceObject(fmt.Sprintf("%s[%d]", path, i), item, 0); err != nil {
			return err
		}
	}
	return nil
}

func validateDeviceObject(path string, raw json.RawMessage, depth int) error {
	if depth > 8 {
		return fieldError(path, "is nested too deeply")
	}
	var obj map[string]json.RawMessage
	if err := decodeJSON(raw, &obj); err != nil {
		return fieldError(path, "must be an object or unknown/not_collected")
	}
	if len(obj) > 64 {
		return fieldError(path, "must contain <= 64 properties")
	}
	for key, value := range obj {
		if err := validateStringBytes(path+" key", key, 1, 128); err != nil {
			return err
		}
		if err := validateDeviceValue(path+"."+key, value, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateDeviceValue(path string, raw json.RawMessage, depth int) error {
	if depth > 8 {
		return fieldError(path, "is nested too deeply")
	}
	if isRawNull(raw) {
		return nil
	}
	if _, ok, err := rawSentinel(raw); err != nil {
		return err
	} else if ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fieldError(path, "must be a valid string")
		}
		return validateStringBytes(path, s, 0, 512)
	}

	switch firstRawByte(raw) {
	case '{':
		return validateDeviceObject(path, raw, depth+1)
	case '[':
		var items []json.RawMessage
		if err := decodeJSON(raw, &items); err != nil {
			return fieldError(path, "must be an array")
		}
		if len(items) > 128 {
			return fieldError(path, "must contain <= 128 items")
		}
		for i, item := range items {
			if err := validateDeviceValue(fmt.Sprintf("%s[%d]", path, i), item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fieldError(path, "must be a valid string")
		}
		return validateStringBytes(path, s, 0, 512)
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return fieldError(path, "must be a boolean")
		}
		return nil
	default:
		var n json.Number
		if err := decodeJSON(raw, &n); err != nil {
			return fieldError(path, "must be a string, number, boolean, null, array, or object")
		}
		if _, err := strconv.ParseFloat(n.String(), 64); err != nil {
			return fieldError(path, "must be a valid number")
		}
		return nil
	}
}

func validateAttrs(category string, raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if isRawNull(raw) {
		return nil, fieldError("log.attrs", "must be an object")
	}

	var attrs map[string]json.RawMessage
	if err := decodeJSON(raw, &attrs); err != nil {
		return nil, fieldError("log.attrs", "must be an object")
	}

	registered := attrRegistry[category]
	if len(registered) == 0 {
		return nil, nil
	}

	filtered := make(map[string]any)
	for key, value := range attrs {
		valueType, ok := registered[key]
		if !ok {
			continue
		}
		decoded, err := validateAttrValue("log.attrs."+key, value, valueType)
		if err != nil {
			return nil, err
		}
		filtered[key] = decoded
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return filtered, nil
}

func validateAttrValue(path string, raw json.RawMessage, valueType attrValueType) (any, error) {
	switch valueType {
	case attrString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fieldError(path, "must be a string")
		}
		if err := validateStringBytes(path, s, 0, 512); err != nil {
			return nil, err
		}
		return s, nil
	case attrInteger:
		if b := firstRawByte(raw); b != '-' && (b < '0' || b > '9') {
			return nil, fieldError(path, "must be an integer")
		}
		var n json.Number
		if err := decodeJSON(raw, &n); err != nil {
			return nil, fieldError(path, "must be an integer")
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return nil, fieldError(path, "must be an integer")
		}
		return i, nil
	default:
		return nil, fieldError(path, "has unsupported registered type %q", valueType)
	}
}

func validateDateTime(path string, value *string) (time.Time, error) {
	s, err := validateRequiredString(path, value, 1, 64)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fieldError(path, "must be RFC3339 date-time")
	}
	return parsed, nil
}

func requiredRawDateTime(raw map[string]json.RawMessage, key, path string) (time.Time, error) {
	s, err := requiredRawString(raw, key, path, 1, 64)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fieldError(path, "must be RFC3339 date-time")
	}
	return parsed, nil
}

func requiredRawString(raw map[string]json.RawMessage, key, path string, minBytes, maxBytes int) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", requiredError(path)
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return "", fieldError(path, "must be a string")
	}
	if err := validateStringBytes(path, s, minBytes, maxBytes); err != nil {
		return "", err
	}
	return s, nil
}

func validateRequiredString(path string, value *string, minBytes, maxBytes int) (string, error) {
	if value == nil {
		return "", requiredError(path)
	}
	if err := validateStringBytes(path, *value, minBytes, maxBytes); err != nil {
		return "", err
	}
	return *value, nil
}

func validateOptionalString(path string, value *string, maxBytes int) (string, error) {
	if value == nil {
		return "", nil
	}
	if err := validateStringBytes(path, *value, 0, maxBytes); err != nil {
		return "", err
	}
	return *value, nil
}

func validateStringBytes(path, value string, minBytes, maxBytes int) error {
	if len(value) < minBytes {
		return fieldError(path, "must be at least %d bytes", minBytes)
	}
	if maxBytes > 0 && len(value) > maxBytes {
		return fieldError(path, "exceeds %d bytes", maxBytes)
	}
	return nil
}

func validateNonNegativeInt64(path string, value *int64) (int64, error) {
	if value == nil {
		return 0, requiredError(path)
	}
	if *value < 0 {
		return 0, fieldError(path, "must be >= 0")
	}
	return *value, nil
}

func validateStringSlice(path string, values []string, maxItems, minStringBytes, maxStringBytes int, unique bool) ([]string, error) {
	if maxItems > 0 && len(values) > maxItems {
		return nil, fieldError(path, "must contain <= %d items", maxItems)
	}
	if unique {
		seen := make(map[string]struct{}, len(values))
		for i, value := range values {
			if _, ok := seen[value]; ok {
				return nil, fieldError(fmt.Sprintf("%s[%d]", path, i), "must be unique")
			}
			seen[value] = struct{}{}
		}
	}
	for i, value := range values {
		if err := validateStringBytes(fmt.Sprintf("%s[%d]", path, i), value, minStringBytes, maxStringBytes); err != nil {
			return nil, err
		}
	}
	return append([]string(nil), values...), nil
}

func validateEnum(path, value string, allowed []string) error {
	if !slices.Contains(allowed, value) {
		return fieldError(path, "unsupported value %q", value)
	}
	return nil
}

func decodeJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid JSON: trailing data")
	}
	return nil
}

func rawSentinel(raw json.RawMessage) (string, bool, error) {
	if firstRawByte(raw) != '"' {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, fieldError("device", "contains invalid string")
	}
	if s == "unknown" || s == "not_collected" {
		return s, true, nil
	}
	return s, true, nil
}

func firstRawByte(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func isRawNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func requiredError(path string) error {
	return fieldError(path, "required")
}

func fieldError(path, format string, args ...any) error {
	return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
}
