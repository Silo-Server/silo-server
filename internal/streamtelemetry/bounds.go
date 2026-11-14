package streamtelemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/clientmetadata"
)

// These are internal telemetry ceilings, not limits on authentication or routing
// identities. Native client labels retain their published character limits.
const (
	maxIdentityBytes        = 256
	maxTagBytes             = 64
	maxPatternBytes         = 1024
	maxTransferIDBytes      = 2048 // composite identity, including NUL separators
	maxRetainedMap          = 32
	maxSessionStringBytes   = 256 * 1024
	maxTransferStringBytes  = 16 * 1024
	maxSessionEncodedBytes  = 2 * 1024 * 1024
	maxTransferEncodedBytes = 128 * 1024
	maxMetaEncodedBytes     = 4096
)

var errRecordBounds = errors.New("stream telemetry record exceeds text bounds")

func validText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func displayDeviceID(value string) string {
	if validText(value, maxIdentityBytes) && !strings.HasPrefix(value, "sha256:") {
		return strings.Clone(value)
	}
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func boundedDisplay(value string, chars int) string {
	return strings.Clone(clientmetadata.Normalize(value, chars))
}

func normalizeCapture(c CaptureSet) CaptureSet {
	c.Method = boundedText(c.Method, maxTagBytes)
	c.Pattern = boundedText(c.Pattern, maxPatternBytes)
	c.ViewerIP = boundedText(c.ViewerIP, maxIdentityBytes)
	c.DeviceID = displayDeviceID(c.DeviceID)
	c.Client.Name = boundedDisplay(c.Client.Name, clientmetadata.Name)
	c.Client.Version = boundedDisplay(c.Client.Version, clientmetadata.Version)
	c.Client.Build = boundedDisplay(c.Client.Build, clientmetadata.Build)
	c.Client.Channel = boundedDisplay(c.Client.Channel, clientmetadata.Channel)
	c.UserAgent = boundedDisplay(c.UserAgent, clientmetadata.UserAgent)
	if !validText(string(c.TokenIssuedFrom), maxTagBytes) {
		c.TokenIssuedFrom = TokenIssuedAtSourceNone
	}
	c.TokenIssuedFrom = TokenIssuedAtSource(strings.Clone(string(c.TokenIssuedFrom)))
	return c
}

func validAttachment(a Attachment) bool {
	return validText(string(a.Subject.Kind), maxTagBytes) && validText(a.Subject.ID, maxIdentityBytes) &&
		validText(a.ProfileID, maxIdentityBytes) && validText(a.SessionID, maxIdentityBytes) &&
		validText(a.PlayMethod, maxTagBytes) && validText(string(a.StartedAtSource), maxTagBytes) &&
		validText(string(a.TokenIssuedAtSource), maxTagBytes)
}

func cloneAttachment(a Attachment) Attachment {
	a.Subject.Kind = SubjectKind(strings.Clone(string(a.Subject.Kind)))
	a.Subject.ID = strings.Clone(a.Subject.ID)
	a.ProfileID = strings.Clone(a.ProfileID)
	a.SessionID = strings.Clone(a.SessionID)
	a.PlayMethod = strings.Clone(a.PlayMethod)
	a.StartedAtSource = StartedAtSource(strings.Clone(string(a.StartedAtSource)))
	a.TokenIssuedAtSource = TokenIssuedAtSource(strings.Clone(string(a.TokenIssuedAtSource)))
	return a
}

// Config is also constructed directly by embedders. Check products without
// multiplication overflow; never silently clamp their existing count policy.
func retainedConfigFits(cfg Config) bool {
	if !wireCollectionsFit(cfg.MaxMediaFileIDsPerSession, cfg.MaxPlayMethodsPerSession, cfg.MaxRoutesPerSession, cfg.MaxViewerIPsPerSession, cfg.MaxDeviceIDsPerSession, cfg.MaxClientVariantsPerSession, cfg.MaxTokenIssuedAtPerSession, cfg.MaxIdentityConflictsPerSession) {
		return false
	}
	remaining := maxSessionStringBytes - (3*maxIdentityBytes + 4*maxTagBytes + 2*maxRetainedMap*maxTagBytes)
	products := []struct{ count, bytes int }{
		{cfg.MaxViewerIPsPerSession, maxIdentityBytes},
		{cfg.MaxDeviceIDsPerSession, maxIdentityBytes},
		{cfg.MaxClientVariantsPerSession, (clientmetadata.Name+clientmetadata.Version+clientmetadata.Build+clientmetadata.Channel)*utf8.UTFMax + clientmetadata.UserAgent*utf8.UTFMax},
		{cfg.MaxPlayMethodsPerSession, maxTagBytes},
		{cfg.MaxRoutesPerSession, maxTagBytes*4 + 2*maxPatternBytes},
		{cfg.MaxIdentityConflictsPerSession, maxTagBytes + 2*maxIdentityBytes},
	}
	for _, product := range products {
		if product.count < 0 || product.count > remaining/product.bytes {
			return false
		}
		remaining -= product.count * product.bytes
	}
	return validText(cfg.PublisherID, maxIdentityBytes) && validText(cfg.NodeID, maxIdentityBytes)
}

type textBudget struct {
	remaining int
	invalid   bool
}

func (b *textBudget) add(value string, maxBytes int) {
	if !validText(value, maxBytes) || len(value) > b.remaining {
		b.invalid = true
		return
	}
	b.remaining -= len(value)
}

func (b *textBudget) label(value string, chars int) {
	b.add(value, chars*utf8.UTFMax)
	if utf8.RuneCountInString(value) > chars {
		b.invalid = true
	}
}

func (b *textBudget) client(c wireClientVariant) {
	b.label(c.Name, clientmetadata.Name)
	b.label(c.Version, clientmetadata.Version)
	b.label(c.Build, clientmetadata.Build)
	b.label(c.Channel, clientmetadata.Channel)
}

func validateSessionText(w wireSession, limit int) error {
	b := textBudget{remaining: limit}
	b.add(string(w.Subject.Kind), maxTagBytes)
	b.add(w.Subject.ID, maxIdentityBytes)
	b.add(w.ProfileID, maxIdentityBytes)
	b.add(w.SessionID, maxIdentityBytes)
	b.add(w.PlayMethod, maxTagBytes)
	b.add(string(w.StartedAtSource), maxTagBytes)
	for _, v := range w.PlayMethods {
		b.add(v, maxTagBytes)
	}
	for _, v := range w.Routes {
		b.add(v.Method, maxTagBytes)
		b.add(v.Pattern, maxPatternBytes)
		b.add(string(v.Role), maxTagBytes)
		b.add(string(v.Class), maxTagBytes)
	}
	for _, v := range w.ViewerIPs {
		b.add(v, maxIdentityBytes)
	}
	for _, v := range w.DeviceIDs {
		b.add(v, maxIdentityBytes)
	}
	for _, v := range w.UserAgents {
		b.label(v, clientmetadata.UserAgent)
	}
	for _, v := range w.ClientVariants {
		b.client(v)
	}
	for _, v := range w.IdentityConflicts {
		b.add(v.Field, maxTagBytes)
		b.add(v.Existing, maxIdentityBytes)
		b.add(v.Offered, maxIdentityBytes)
	}
	for k := range w.TokenIssuedAtSources {
		b.add(string(k), maxTagBytes)
	}
	for k := range w.Outcomes {
		b.add(string(k), maxTagBytes)
	}
	if b.invalid {
		return errRecordBounds
	}
	return nil
}

func validateTransferText(w wireTransfer, limit int) error {
	b := textBudget{remaining: limit}
	// Only the generated composite transfer key may carry NUL separators.
	if len(w.ID) > maxTransferIDBytes {
		return errRecordBounds
	}
	if !validText(strings.ReplaceAll(w.ID, "\x00", ""), maxTransferIDBytes) || len(w.ID) > b.remaining {
		return errRecordBounds
	}
	b.remaining -= len(w.ID)
	b.add(string(w.Subject.Kind), maxTagBytes)
	b.add(w.Subject.ID, maxIdentityBytes)
	b.add(w.ProfileID, maxIdentityBytes)
	b.add(w.Method, maxTagBytes)
	b.add(w.Pattern, maxPatternBytes)
	b.add(string(w.Role), maxTagBytes)
	b.add(w.ViewerIP, maxIdentityBytes)
	b.add(w.DeviceID, maxIdentityBytes)
	b.client(w.Client)
	b.label(w.UserAgent, clientmetadata.UserAgent)
	for k := range w.Outcomes {
		b.add(string(k), maxTagBytes)
	}
	if b.invalid {
		return errRecordBounds
	}
	return nil
}

func boundedText(value string, maxBytes int) string {
	value = clientmetadata.Normalize(value, maxBytes)
	if len(value) > maxBytes {
		end := maxBytes
		for !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	return strings.Clone(value)
}

func incrementBounded[K ~string](values map[K]int64, key K) bool {
	if !validText(string(key), maxTagBytes) {
		return false
	}
	if _, ok := values[key]; !ok {
		if len(values) >= maxRetainedMap {
			return false
		}
		key = K(strings.Clone(string(key)))
	}
	values[key]++
	return true
}
