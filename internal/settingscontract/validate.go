package settingscontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks every invariant the manifest schema cannot express: that
// resolution orders are consistent with allowed scopes, that defaults satisfy
// their own value schemas, that revision tags are internally ordered, and that
// policy constraints are applicable to the type they are declared on.
//
// A failure here is a defect in the checked-in contract. It is surfaced by the
// contract tests, and by startup if it somehow ships.
func (m *Manifest) Validate(objectSchemas map[string]*jsonschema.Schema) error {
	var errs []error

	if m.APIVersion < 1 {
		errs = append(errs, fmt.Errorf("api_version must be at least 1, got %d", m.APIVersion))
	}
	if m.Revision < 1 {
		errs = append(errs, fmt.Errorf("revision must be at least 1, got %d", m.Revision))
	}
	if len(m.Definitions) == 0 {
		errs = append(errs, errors.New("manifest declares no definitions"))
	}

	for i := range m.Definitions {
		def := &m.Definitions[i]
		if err := def.validate(m.Revision, objectSchemas); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", def.Key, err))
		}
	}

	return errors.Join(errs...)
}

func (d *Definition) validate(manifestRevision int, objectSchemas map[string]*jsonschema.Schema) error {
	var errs []error

	if d.IntroducedIn < 1 || d.IntroducedIn > manifestRevision {
		errs = append(errs, fmt.Errorf(
			"introduced_in %d is outside 1..%d (the manifest revision)", d.IntroducedIn, manifestRevision))
	}

	errs = append(errs, d.validateScopes(manifestRevision)...)
	errs = append(errs, d.validateResolutionOrder()...)
	errs = append(errs, d.ValueSchema.validate(manifestRevision, objectSchemas)...)
	errs = append(errs, d.validateDefault(objectSchemas)...)
	errs = append(errs, d.validateConstraint()...)

	return errors.Join(errs...)
}

func (d *Definition) validateScopes(manifestRevision int) []error {
	var errs []error

	if len(d.AllowedScopes) == 0 {
		return []error{errors.New("allowed_scopes is empty")}
	}

	seen := make(map[Scope]struct{}, len(d.AllowedScopes))
	for _, entry := range d.AllowedScopes {
		if _, dup := seen[entry.Scope]; dup {
			errs = append(errs, fmt.Errorf("allowed_scopes repeats %q", entry.Scope))
		}
		seen[entry.Scope] = struct{}{}

		if entry.IntroducedIn != 0 {
			if entry.IntroducedIn < d.IntroducedIn {
				errs = append(errs, fmt.Errorf(
					"scope %q claims introduced_in %d, before the definition's own %d",
					entry.Scope, entry.IntroducedIn, d.IntroducedIn))
			}
			if entry.IntroducedIn > manifestRevision {
				errs = append(errs, fmt.Errorf(
					"scope %q claims introduced_in %d, after the manifest revision %d",
					entry.Scope, entry.IntroducedIn, manifestRevision))
			}
		}
	}

	// Persistence and scope have to agree, or a client cannot tell where a value
	// lives from the definition alone.
	switch d.Persistence {
	case PersistenceRemote:
		for _, entry := range d.AllowedScopes {
			if !entry.Scope.IsRemote() {
				errs = append(errs, fmt.Errorf(
					"remote setting allows non-remote scope %q", entry.Scope))
			}
		}
	case PersistenceClientLocal:
		if len(d.AllowedScopes) != 1 || d.AllowedScopes[0].Scope != ScopeClientLocal {
			errs = append(errs, errors.New(
				`client_local setting must declare exactly one scope, "client_local"`))
		}
		if d.ConstrainedBy != nil {
			errs = append(errs, errors.New(
				"client_local setting declares constrained_by, but the server never resolves it"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown persistence %q", d.Persistence))
	}

	return errs
}

func (d *Definition) validateResolutionOrder() []error {
	var errs []error

	order := d.ResolutionOrder
	if len(order) == 0 {
		return []error{errors.New("resolution_order is empty")}
	}
	if order[len(order)-1] != ScopeDefault {
		return []error{fmt.Errorf(
			"resolution_order must end with %q, got %q", ScopeDefault, order[len(order)-1])}
	}

	seen := make(map[Scope]struct{}, len(order))
	for _, scope := range order[:len(order)-1] {
		if _, dup := seen[scope]; dup {
			errs = append(errs, fmt.Errorf("resolution_order repeats %q", scope))
		}
		seen[scope] = struct{}{}

		if scope == ScopeDefault {
			errs = append(errs, errors.New(`resolution_order lists "default" before the end`))
			continue
		}
		if !d.AllowsScope(scope) {
			errs = append(errs, fmt.Errorf(
				"resolution_order resolves %q, which is not in allowed_scopes", scope))
		}
	}

	// Every scope a value can be written at must be reachable when reading it,
	// or the setting accepts writes it will never honor.
	for _, entry := range d.AllowedScopes {
		if _, ok := seen[entry.Scope]; !ok {
			errs = append(errs, fmt.Errorf(
				"scope %q is writable but never read: missing from resolution_order", entry.Scope))
		}
	}

	return errs
}

func (v *ValueSchema) validate(manifestRevision int, objectSchemas map[string]*jsonschema.Schema) []error {
	var errs []error

	switch v.Type {
	case TypeBoolean, TypeLanguageTag:
		// No constraints beyond nullability.

	case TypeInteger, TypeNumber:
		if v.Minimum == nil || v.Maximum == nil {
			errs = append(errs, fmt.Errorf("%s requires minimum and maximum", v.Type))
			break
		}
		if *v.Minimum > *v.Maximum {
			errs = append(errs, fmt.Errorf(
				"minimum %g exceeds maximum %g", *v.Minimum, *v.Maximum))
		}
		if v.Step != nil && *v.Step <= 0 {
			errs = append(errs, fmt.Errorf("step must be positive, got %g", *v.Step))
		}
		// A slice, not a map: ranging a map would order these two errors
		// randomly, so the same broken manifest would report differently from
		// run to run.
		for _, tagged := range []struct {
			label string
			rev   int
		}{
			{"minimum_introduced_in", v.MinimumIntroducedIn},
			{"maximum_introduced_in", v.MaximumIntroducedIn},
		} {
			if tagged.rev != 0 && tagged.rev > manifestRevision {
				errs = append(errs, fmt.Errorf(
					"%s is %d, after the manifest revision %d",
					tagged.label, tagged.rev, manifestRevision))
			}
		}

	case TypeString:
		if v.MaxLength == nil {
			errs = append(errs, errors.New("string requires max_length"))
		} else if *v.MaxLength < 1 {
			errs = append(errs, fmt.Errorf("max_length must be positive, got %d", *v.MaxLength))
		}
		if v.MinLength != nil && v.MaxLength != nil && *v.MinLength > *v.MaxLength {
			errs = append(errs, fmt.Errorf(
				"min_length %d exceeds max_length %d", *v.MinLength, *v.MaxLength))
		}
		if v.Pattern != "" {
			// Compiled once here and reused by every ValidateValue call.
			// ValidateValue is documented as the per-request validation path,
			// so recompiling the pattern on each call would put a regex
			// compile on every settings write.
			compiled, err := regexp.Compile(v.Pattern)
			if err != nil {
				errs = append(errs, fmt.Errorf("pattern does not compile: %w", err))
			} else {
				v.compiledPattern = compiled
			}
		}

	case TypeEnum:
		if len(v.Values) == 0 {
			errs = append(errs, errors.New("enum requires at least one member"))
		}
		seen := make(map[string]struct{}, len(v.Values))
		for _, member := range v.Values {
			// Type-tagged, so a string member "3" and an integer member 3 are
			// two distinct members rather than a reported duplicate.
			token := enumToken(member.Value)
			if _, dup := seen[token]; dup {
				errs = append(errs, fmt.Errorf("enum repeats value %s", displayEnumValue(member.Value)))
			}
			seen[token] = struct{}{}
			if member.IntroducedIn != 0 && member.IntroducedIn > manifestRevision {
				errs = append(errs, fmt.Errorf(
					"enum member %s claims introduced_in %d, after the manifest revision %d",
					displayEnumValue(member.Value), member.IntroducedIn, manifestRevision))
			}
		}

	case TypeObject:
		if v.SchemaRef == "" {
			errs = append(errs, errors.New("object requires schema_ref"))
			break
		}
		if _, ok := objectSchemas[v.SchemaRef]; !ok {
			errs = append(errs, fmt.Errorf(
				"schema_ref %q has no file under contracts/settings/v1/schemas", v.SchemaRef))
		}

	default:
		errs = append(errs, fmt.Errorf("unknown value type %q", v.Type))
	}

	return errs
}

func (d *Definition) validateDefault(objectSchemas map[string]*jsonschema.Schema) []error {
	raw := bytes.TrimSpace(d.DefaultValue)
	if len(raw) == 0 {
		return []error{errors.New("default_value is required; use null for a nullable setting")}
	}

	if bytes.Equal(raw, []byte("null")) {
		if !d.ValueSchema.Nullable {
			return []error{errors.New("default_value is null but the value schema is not nullable")}
		}
		return nil
	}

	if err := d.ValueSchema.ValidateValue(raw, objectSchemas); err != nil {
		return []error{fmt.Errorf("default_value is invalid: %w", err)}
	}
	return nil
}

func (d *Definition) validateConstraint() []error {
	if d.ConstrainedBy == nil {
		return nil
	}

	var errs []error
	switch d.ConstrainedBy.Constraint {
	case ConstraintCeiling, ConstraintFloor:
		// Capping a value only means something where values are comparable.
		// Declaring a ceiling on an unordered enum silently does nothing, which
		// is worse than refusing it.
		ordered := d.ValueSchema.Type == TypeInteger ||
			d.ValueSchema.Type == TypeNumber ||
			(d.ValueSchema.Type == TypeEnum && d.ValueSchema.Ordered)
		if !ordered {
			errs = append(errs, fmt.Errorf(
				"%s constraint requires a numeric type or an ordered enum, got %s",
				d.ConstrainedBy.Constraint, d.ValueSchema.Type))
		}
	case ConstraintAllowlist, ConstraintLocked:
		// Applicable to any type.
	default:
		errs = append(errs, fmt.Errorf("unknown constraint %q", d.ConstrainedBy.Constraint))
	}

	if strings.TrimSpace(d.ConstrainedBy.PolicyInput) == "" {
		errs = append(errs, errors.New("constrained_by requires a policy_input"))
	}

	return errs
}

// ValidateValue checks a JSON value against this schema. It is the single
// validation path: the mutation endpoint, the migration, and the manifest's own
// default checks all use it, so a value that validates in one place validates
// everywhere.
func (v *ValueSchema) ValidateValue(raw json.RawMessage, objectSchemas map[string]*jsonschema.Schema) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		if v.Nullable {
			return nil
		}
		return errors.New("null is not allowed for this setting")
	}

	switch v.Type {
	case TypeBoolean:
		var value bool
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected a boolean: %w", err)
		}

	case TypeInteger:
		var value json.Number
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected an integer: %w", err)
		}
		parsed, err := value.Int64()
		if err != nil {
			return fmt.Errorf("expected an integer, got %s", value)
		}
		return v.checkRange(float64(parsed))

	case TypeNumber:
		var value json.Number
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected a number: %w", err)
		}
		parsed, err := value.Float64()
		if err != nil {
			return fmt.Errorf("expected a number, got %s", value)
		}
		return v.checkRange(parsed)

	case TypeString:
		var value string
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected a string: %w", err)
		}
		return v.checkString(value)

	case TypeEnum:
		var value any
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected an enum value: %w", err)
		}
		for _, member := range v.Values {
			if enumMatches(value, member.Value) {
				return nil
			}
		}
		return fmt.Errorf("%s is not one of %s", displayEnumValue(value), v.enumTokens())

	case TypeLanguageTag:
		var value string
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected a language tag: %w", err)
		}
		if _, ok := NormalizeLanguageTag(value); !ok {
			return fmt.Errorf("%q is not a well-formed BCP 47 language tag", value)
		}

	case TypeObject:
		schema, ok := objectSchemas[v.SchemaRef]
		if !ok {
			return fmt.Errorf("no compiled schema for %q", v.SchemaRef)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(trimmed))
		if err != nil {
			return fmt.Errorf("expected an object: %w", err)
		}
		if err := schema.Validate(doc); err != nil {
			return fmt.Errorf("does not satisfy %s: %w", v.SchemaRef, err)
		}

	default:
		return fmt.Errorf("unknown value type %q", v.Type)
	}

	return nil
}

// NormalizeValue validates a value and returns the form that should be stored.
//
// Anything that persists a value goes through here rather than ValidateValue,
// so the row that lands in the database is the one every client compares
// against. Only language tags differ from their input today; every other type
// is already canonical once it validates.
func (v *ValueSchema) NormalizeValue(
	raw json.RawMessage,
	objectSchemas map[string]*jsonschema.Schema,
) (json.RawMessage, error) {
	if err := v.ValidateValue(raw, objectSchemas); err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(raw)
	if v.Type != TypeLanguageTag || bytes.Equal(trimmed, []byte("null")) {
		return append(json.RawMessage(nil), trimmed...), nil
	}

	var tag string
	if err := strictUnmarshal(trimmed, &tag); err != nil {
		return nil, fmt.Errorf("expected a language tag: %w", err)
	}
	normalized, ok := NormalizeLanguageTag(tag)
	if !ok {
		return nil, fmt.Errorf("%q is not a well-formed BCP 47 language tag", tag)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encoding normalized language tag: %w", err)
	}
	return encoded, nil
}

// stepTolerance absorbs binary floating-point error when checking a value
// against a declared step. 0.05 is not exactly representable, so requiring an
// exact multiple would reject values every client can legitimately produce.
const stepTolerance = 1e-9

func (v *ValueSchema) checkRange(value float64) error {
	if v.Minimum != nil && value < *v.Minimum {
		return fmt.Errorf("%g is below the minimum %g", value, *v.Minimum)
	}
	if v.Maximum != nil && value > *v.Maximum {
		return fmt.Errorf("%g is above the maximum %g", value, *v.Maximum)
	}
	if v.Step != nil && *v.Step > 0 {
		// Steps are counted from the minimum, which is the only origin every
		// client's stepper agrees on.
		base := 0.0
		if v.Minimum != nil {
			base = *v.Minimum
		}
		steps := (value - base) / *v.Step
		if math.Abs(steps-math.Round(steps))**v.Step > stepTolerance {
			return fmt.Errorf("%g is not a multiple of the step %g from %g",
				value, *v.Step, base)
		}
	}
	return nil
}

func (v *ValueSchema) checkString(value string) error {
	length := len([]rune(value))
	if v.MinLength != nil && length < *v.MinLength {
		return fmt.Errorf("is shorter than the minimum %d characters", *v.MinLength)
	}
	if v.MaxLength != nil && length > *v.MaxLength {
		return fmt.Errorf("is longer than the maximum %d characters", *v.MaxLength)
	}
	if v.Pattern != "" {
		matcher := v.compiledPattern
		if matcher == nil {
			// Only reachable for a schema built in a test rather than loaded
			// from the manifest, where validate() would have compiled it.
			compiled, err := regexp.Compile(v.Pattern)
			if err != nil {
				return fmt.Errorf("pattern does not compile: %w", err)
			}
			matcher = compiled
		}
		if !matcher.MatchString(value) {
			return fmt.Errorf("does not match %s", v.Pattern)
		}
	}
	return nil
}

func (v *ValueSchema) enumTokens() string {
	tokens := make([]string, 0, len(v.Values))
	for _, member := range v.Values {
		tokens = append(tokens, displayEnumValue(member.Value))
	}
	return strings.Join(tokens, ", ")
}

// enumMatches reports whether a decoded request value is the same JSON value as
// an enum member.
//
// Comparison is by JSON type and value rather than by formatted text.
// manifest.schema.json permits string, integer and boolean members, and
// comparing "%v" tokens would let the string "3" satisfy an integer member 3
// and the string "true" satisfy a boolean member — storing a wire value of a
// type every generated binding would then fail to decode.
func enumMatches(value, member any) bool {
	switch got := value.(type) {
	case string:
		want, ok := member.(string)
		return ok && got == want
	case bool:
		want, ok := member.(bool)
		return ok && got == want
	case json.Number:
		return numberEqualsMember(got, member)
	default:
		return false
	}
}

// numberEqualsMember compares numerically, so a member written 1e6 matches a
// request sending 1000000 and an integer member 3 matches 3.0. Manifest members
// decode without UseNumber and arrive as float64; values built in tests may
// already be json.Number.
func numberEqualsMember(value json.Number, member any) bool {
	var want float64
	switch typed := member.(type) {
	case float64:
		want = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return false
		}
		want = parsed
	default:
		return false
	}
	got, err := value.Float64()
	if err != nil {
		return false
	}
	return got == want
}

// enumToken is a type-tagged identity for duplicate detection, so a string
// member and a numeric member that print the same are not conflated.
func enumToken(value any) string {
	switch typed := value.(type) {
	case string:
		return "s:" + typed
	case bool:
		return "b:" + strconv.FormatBool(typed)
	case float64:
		return "n:" + strconv.FormatFloat(typed, 'g', -1, 64)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return "n:" + strconv.FormatFloat(parsed, 'g', -1, 64)
		}
		return "n:" + typed.String()
	default:
		return fmt.Sprintf("?:%v", value)
	}
}

// displayEnumValue renders a member for an error message, quoting strings so a
// reader can tell "3" from 3.
func displayEnumValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

// strictUnmarshal rejects trailing content and, for numbers, preserves the
// literal so an integer field cannot silently accept 1.5.
func strictUnmarshal(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	// Not decoder.More(): that reports whether another element follows in the
	// *current* array or object, so it answers false for a stray "]" or "}" —
	// exactly the bytes a caller slicing a value out of a larger document is
	// most likely to hand over, which would let `true]` validate as a boolean.
	// Reading the next token tolerates only end-of-input.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing content")
	}
	return nil
}

// languageTagPattern accepts the well-formed BCP 47 shapes real clients
// produce: language, script, region, variants, extension singletons and
// private use. The narrower language[-script][-region] form this started as
// rejected tags Android and iOS emit unprompted — `Locale.toLanguageTag()`
// appends extension subtags for a non-Gregorian calendar or non-Latin numbering
// system (`ar-EG-u-nu-latn`), and registered variants like `ca-ES-valencia` are
// ordinary user choices.
var languageTagPattern = regexp.MustCompile(
	`^[a-zA-Z]{2,3}(-[a-zA-Z]{4})?(-([a-zA-Z]{2}|[0-9]{3}))?` +
		`(-([0-9a-zA-Z]{5,8}|[0-9][0-9a-zA-Z]{3}))*` +
		`(-[0-9a-wy-zA-WY-Z](-[0-9a-zA-Z]{2,8})+)*` +
		`(-[xX](-[0-9a-zA-Z]{1,8})+)?$`)

// NormalizeLanguageTag returns the canonical BCP 47 form of a tag, or false if
// it is not well-formed.
//
// Normalization is the half that keeps the contract's promise of one stored
// value per language. Without it `en-US`, `en-us` and `EN-us` are three
// distinct rows for one preference, and audio-track matching misses on two of
// them. Underscores are accepted on input because both mobile platforms have a
// locale accessor that produces them (`Locale.identifier` on iOS,
// `Locale.toString()` on Android) and sending one is a mistake worth absorbing
// rather than a value worth rejecting.
//
// The empty string is not a language tag. "No preference" is null, which the
// nullable flag on each language setting already expresses.
func NormalizeLanguageTag(tag string) (string, bool) {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	if !languageTagPattern.MatchString(tag) {
		return "", false
	}

	parts := strings.Split(tag, "-")
	// Case is not significant in BCP 47, but the conventional casing is what
	// every client library produces: lowercase language, Titlecase script,
	// UPPERCASE region, lowercase everything else.
	parts[0] = strings.ToLower(parts[0])
	inExtension := false
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		switch {
		case len(part) == 1:
			// A singleton opens an extension ("u", "t") or private use ("x").
			// Everything after it is extension content, so the two-letter
			// region rule must stop applying — "nu" in "ar-EG-u-nu-latn" is an
			// extension key, not a region.
			inExtension = true
			parts[i] = strings.ToLower(part)
		case inExtension:
			parts[i] = strings.ToLower(part)
		case i == 1 && len(part) == 4 && isAlpha(part):
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		case len(part) == 2 && isAlpha(part):
			parts[i] = strings.ToUpper(part)
		default:
			parts[i] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "-"), true
}

func isAlpha(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
