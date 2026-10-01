package contractspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The semantic diff is oasdiff (github.com/oasdiff/oasdiff), pinned by exact
// version in go.mod and by checksum in go.sum like every other dependency;
// TestDiffToolIsPinned refuses a replace directive or a pseudo-version.
// Upgrading it is a dedicated dependency PR: the seeded fixture in
// testdata/breaking must still be detected before the upgrade is accepted.

// Change is one backward-compatibility finding, reduced to what the policy
// needs.
type Change struct {
	// ID is oasdiff's rule id (for example api-removed-without-deprecation).
	ID string
	// Level is oasdiff's level name: ERR, WARN or INFO.
	Level string
	// Breaking is true for ERR and WARN, the same set `oasdiff breaking`
	// reports.
	Breaking bool
	// Method and Path locate the operation; empty for a component change.
	Method string
	Path   string
	// OperationID is the operation's id; empty for a component change.
	OperationID string
	// Text is the human-readable finding.
	Text string
	// Fingerprint identifies this exact change for an approval entry: the
	// SHA-256 of the rule id, the operation, and the rule's arguments.
	Fingerprint string
}

// Diff compares two OpenAPI documents and lists every finding, sorted by
// operation, rule and text so the output is stable.
func Diff(base, revision []byte) ([]Change, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	s1, err := load.NewSpecInfoFromData(loader, base, "base.json")
	if err != nil {
		return nil, fmt.Errorf("base: %w", err)
	}
	s2, err := load.NewSpecInfoFromData(loader, revision, "revision.json")
	if err != nil {
		return nil, fmt.Errorf("revision: %w", err)
	}
	config := diff.NewConfig()
	config.IncludePathParams = true
	report, sources, err := diff.GetWithOperationsSourcesMap(config, s1, s2)
	if err != nil {
		return nil, err
	}
	findings := checker.CheckBackwardCompatibilityUntilLevel(checker.NewConfig(checker.GetAllChecks()), report, sources, checker.INFO)
	localizer := checker.NewDefaultLocalizer()
	out := make([]Change, 0, len(findings))
	for _, f := range findings {
		c := Change{
			ID:          f.GetId(),
			Level:       f.GetLevel().String(),
			Breaking:    f.GetLevel().IsBreaking(),
			Method:      f.GetOperation(),
			Path:        f.GetPath(),
			OperationID: f.GetOperationId(),
			Text:        f.GetUncolorizedText(localizer),
		}
		c.Fingerprint = fingerprint(c, f.GetArgs())
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Text < b.Text
	})
	return out, nil
}

func fingerprint(c Change, args []any) string {
	h := sha256.New()
	for _, part := range []string{c.ID, c.Method, c.Path, c.OperationID} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	for _, a := range args {
		_, _ = fmt.Fprint(h, a)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Approval is one committed allowlist entry: exactly one change on exactly
// one operation, by fingerprint. Nothing about it is a pattern.
type Approval struct {
	OperationID string `json:"operation_id"`
	ChangeID    string `json:"change_id"`
	Fingerprint string `json:"fingerprint"`
	Reason      string `json:"reason"`
	ApprovedIn  string `json:"approved_in"`
}

// ApprovalsFile is contracts/api/v2/breaking-approvals.json.
type ApprovalsFile struct {
	Approvals []Approval `json:"approvals"`
}

// ApprovalsPath and ApprovalsSchemaPath are the artifact and its schema
// inside contracts/api/v2. LockMarkerPath is the post-lock marker: when it
// exists the contract is 1.0-locked and no approval overrides a breaking
// change.
const (
	ApprovalsPath       = "breaking-approvals.json"
	ApprovalsSchemaPath = "breaking-approvals.schema.json"
	LockMarkerPath      = "LOCKED"

	approvalsSchemaID = "https://siloserver.org/contracts/api/v2/breaking-approvals.schema.json"
)

var (
	lowerCamel = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	hexDigest  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ruleID     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// LoadApprovals reads and validates the allowlist against its JSON Schema,
// then re-checks in Go what the schema states, so a prose-only entry (no
// fingerprint), a wildcard, an empty reason or a duplicate fingerprint is
// refused whichever file is wrong.
func LoadApprovals(fsys fs.FS) (*ApprovalsFile, error) {
	raw, err := fs.ReadFile(fsys, ApprovalsPath)
	if err != nil {
		return nil, err
	}
	schemaBytes, err := fs.ReadFile(fsys, ApprovalsSchemaPath)
	if err != nil {
		return nil, err
	}
	return ParseApprovals(raw, schemaBytes)
}

// ParseApprovals is LoadApprovals over bytes.
func ParseApprovals(raw, schemaBytes []byte) (*ApprovalsFile, error) {
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ApprovalsSchemaPath, err)
	}
	// The schema is registered under its own $id so a validation error
	// names the contract URL, not a working-tree path.
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(approvalsSchemaID, schemaDoc); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(approvalsSchemaID)
	if err != nil {
		return nil, err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ApprovalsPath, err)
	}
	if err := schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("%s violates its schema: %w", ApprovalsPath, err)
	}
	var file ApprovalsFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%s: %w", ApprovalsPath, err)
	}
	seen := map[string]bool{}
	for i, a := range file.Approvals {
		switch {
		case !lowerCamel.MatchString(a.OperationID):
			return nil, fmt.Errorf("approvals[%d]: operation_id %q must name one operation, exactly", i, a.OperationID)
		case !ruleID.MatchString(a.ChangeID):
			return nil, fmt.Errorf("approvals[%d]: change_id %q must be one oasdiff rule id", i, a.ChangeID)
		case !hexDigest.MatchString(a.Fingerprint):
			return nil, fmt.Errorf("approvals[%d]: fingerprint must be the 64-hex change fingerprint the diff prints", i)
		case strings.TrimSpace(a.Reason) == "":
			return nil, fmt.Errorf("approvals[%d]: reason is required", i)
		case seen[a.Fingerprint]:
			return nil, fmt.Errorf("approvals[%d]: fingerprint %s is listed twice", i, a.Fingerprint)
		}
		seen[a.Fingerprint] = true
	}
	return &file, nil
}

// ErrBreaking is returned by Policy when an unapproved breaking change is
// present; the message lists each one with its fingerprint.
var ErrBreaking = errors.New("breaking contract change")

// Policy decides the diff. Pre-lock, every breaking change needs an approval
// whose operation id, change id and fingerprint all match. Post-lock
// (locked), any breaking change fails and approvals are not consulted: the
// only route is the Deprecation/Sunset flow, not an allowlist. An approval
// that matches nothing is also an error, so stale entries cannot accumulate.
func Policy(changes []Change, approvals *ApprovalsFile, locked bool) error {
	approved := map[string]Approval{}
	if approvals != nil {
		for _, a := range approvals.Approvals {
			approved[a.Fingerprint] = a
		}
	}
	used := map[string]bool{}
	var failures []string
	for _, c := range changes {
		if !c.Breaking {
			continue
		}
		line := fmt.Sprintf("%s %s %s [%s %s] fingerprint=%s: %s", c.Level, c.Method, c.Path, c.OperationID, c.ID, c.Fingerprint, c.Text)
		if locked {
			failures = append(failures, "locked contract: "+line)
			continue
		}
		a, ok := approved[c.Fingerprint]
		if !ok || a.OperationID != c.OperationID || a.ChangeID != c.ID {
			failures = append(failures, "unapproved: "+line)
			continue
		}
		used[c.Fingerprint] = true
	}
	if !locked {
		for fp, a := range approved {
			if !used[fp] {
				failures = append(failures, fmt.Sprintf("stale approval for %s %s (fingerprint=%s) matches no change; remove it", a.OperationID, a.ChangeID, fp))
			}
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return fmt.Errorf("%w:\n  %s", ErrBreaking, strings.Join(failures, "\n  "))
}
