package settingscontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
		for label, rev := range map[string]int{
			"minimum_introduced_in": v.MinimumIntroducedIn,
			"maximum_introduced_in": v.MaximumIntroducedIn,
		} {
			if rev != 0 && rev > manifestRevision {
				errs = append(errs, fmt.Errorf(
					"%s is %d, after the manifest revision %d", label, rev, manifestRevision))
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
			if _, err := regexp.Compile(v.Pattern); err != nil {
				errs = append(errs, fmt.Errorf("pattern does not compile: %w", err))
			}
		}

	case TypeEnum:
		if len(v.Values) == 0 {
			errs = append(errs, errors.New("enum requires at least one member"))
		}
		seen := make(map[string]struct{}, len(v.Values))
		for _, member := range v.Values {
			token := fmt.Sprintf("%v", member.Value)
			if _, dup := seen[token]; dup {
				errs = append(errs, fmt.Errorf("enum repeats value %q", token))
			}
			seen[token] = struct{}{}
			if member.IntroducedIn != 0 && member.IntroducedIn > manifestRevision {
				errs = append(errs, fmt.Errorf(
					"enum member %q claims introduced_in %d, after the manifest revision %d",
					token, member.IntroducedIn, manifestRevision))
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
		token := fmt.Sprintf("%v", value)
		for _, member := range v.Values {
			if fmt.Sprintf("%v", member.Value) == token {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of %s", token, v.enumTokens())

	case TypeLanguageTag:
		var value string
		if err := strictUnmarshal(trimmed, &value); err != nil {
			return fmt.Errorf("expected a language tag: %w", err)
		}
		if !languageTagPattern.MatchString(value) {
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

func (v *ValueSchema) checkRange(value float64) error {
	if v.Minimum != nil && value < *v.Minimum {
		return fmt.Errorf("%g is below the minimum %g", value, *v.Minimum)
	}
	if v.Maximum != nil && value > *v.Maximum {
		return fmt.Errorf("%g is above the maximum %g", value, *v.Maximum)
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
		matcher, err := regexp.Compile(v.Pattern)
		if err != nil {
			return fmt.Errorf("pattern does not compile: %w", err)
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
		tokens = append(tokens, fmt.Sprintf("%v", member.Value))
	}
	return strings.Join(tokens, ", ")
}

// strictUnmarshal rejects trailing content and, for numbers, preserves the
// literal so an integer field cannot silently accept 1.5.
func strictUnmarshal(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("unexpected trailing content")
	}
	return nil
}

// languageTagPattern accepts well-formed BCP 47 tags of the shapes Silo
// actually stores: language, language-region, and language-script-region.
// Full RFC 5646 grammar is deliberately not implemented; anything this rejects
// is a value no client currently produces.
var languageTagPattern = regexp.MustCompile(
	`^[a-zA-Z]{2,3}(-[a-zA-Z]{4})?(-([a-zA-Z]{2}|[0-9]{3}))?$`)
