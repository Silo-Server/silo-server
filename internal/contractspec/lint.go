// Package contractspec is the gate over the committed native API v2 OpenAPI
// artifact: the spec lint (Lint) and the semantic diff policy (Diff,
// Approvals). Both read the document as JSON; neither needs the router.
package contractspec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/apiv2"
)

// Extension names the document carries; they mirror internal/apiv2.
const (
	extClass          = "x-silo-class"
	extDemoRestricted = "x-silo-demo-restricted"
	extServiceBacked  = "x-silo-service-backed"
	extGuarded        = "x-silo-guarded"
	extConditional    = "x-silo-conditional"
	extCreateOnly     = "x-silo-create-only"
	extExtensionBag   = "x-silo-extension-bag"
	extDeprecation    = "x-silo-deprecation"

	// Members of x-silo-deprecation.
	deprecationAt     = "at"
	deprecationLink   = "link"
	deprecationSunset = "sunset"
	securityScheme    = "bearerAuth"
)

const (
	typeObject = "object"
	typeString = "string"
)

// Lint checks the generated document against the contract's shape rules and
// returns every violation, sorted, so a report names them all at once. The
// rules: every operation names a unique lowerCamelCase operationId; every
// top-level schema is PascalCase and every response schema is a $ref to one
// (or a named extension bag); every operation documents a success status and
// each status its class implies; non-public operations require the bearer
// scheme and public ones carry no security; no schema is a free-form object
// unless it is marked as an extension bag; date, URL, and language or
// country members declare their shape (lintValueShape).
func Lint(doc []byte) []string {
	var d document
	if err := json.Unmarshal(doc, &d); err != nil {
		return []string{"document is not JSON: " + err.Error()}
	}
	var out []string
	fail := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	ids := map[string]string{}
	for _, path := range sortedKeys(d.Paths) {
		for _, method := range sortedKeys(d.Paths[path]) {
			op := d.Paths[path][method]
			where := strings.ToUpper(method) + " " + path
			if op.OperationID == "" {
				fail("%s: operation has no operationId (implicit ids are refused)", where)
			} else {
				if !isLowerCamel(op.OperationID) {
					fail("%s: operationId %q is not lowerCamelCase", where, op.OperationID)
				}
				if prev, dup := ids[op.OperationID]; dup {
					fail("%s: operationId %q duplicates %s", where, op.OperationID, prev)
				}
				ids[op.OperationID] = where
			}
			lintStatuses(fail, where, path, method, op)
			lintSecurity(fail, where, op)
			lintDeprecation(fail, where, op)
			for _, p := range op.Parameters {
				lintValueShape(fail, where+" parameter "+p.Name, p.Name, p.Description, p.Schema)
			}
			if op.RequestBody != nil {
				for mt, media := range op.RequestBody.Content {
					lintNamed(fail, where+" request "+mt, media.Schema)
				}
			}
			for status, resp := range op.Responses {
				for mt, media := range resp.Content {
					lintNamed(fail, where+" response "+status+" "+mt, media.Schema)
				}
			}
		}
	}
	for _, name := range sortedKeys(d.Components.Schemas) {
		if !isPascal(name) {
			fail("components.schemas.%s: schema name is not PascalCase", name)
		}
		lintFreeForm(fail, "components.schemas."+name, d.Components.Schemas[name])
		lintMemberShapes(fail, "components.schemas."+name, d.Components.Schemas[name])
	}
	if _, ok := d.Components.SecuritySchemes[securityScheme]; !ok {
		fail("components.securitySchemes.%s is missing", securityScheme)
	}
	sort.Strings(out)
	return out
}

func lintStatuses(fail func(string, ...any), where, path, method string, op operation) {
	class := apiv2.Class(stringExt(op.Extensions, extClass))
	if class == "" {
		fail("%s: %s is missing", where, extClass)
		return
	}
	demo, _ := op.Extensions[extDemoRestricted].(bool)
	serviceBacked, _ := op.Extensions[extServiceBacked].(bool)
	guarded, _ := op.Extensions[extGuarded].(bool)
	conditional, _ := op.Extensions[extConditional].(bool)
	createOnly, _ := op.Extensions[extCreateOnly].(bool)
	protocol := stringExt(op.Extensions, "x-silo-raw-protocol")
	success := false
	for status := range op.Responses {
		if code, err := strconv.Atoi(status); err == nil && ((code >= 200 && code < 300) || (protocol != "" && apiv2.RawSuccessStatus(strings.ToUpper(method), protocol, code))) {
			success = true
		}
	}
	if !success {
		fail("%s: no success status is documented", where)
	}
	implied := apiv2.ImpliedStatuses(class, demo, serviceBacked, op.RequestBody != nil, strings.Contains(path, "{"), guarded, conditional, createOnly)
	if protocol != "" {
		if strings.TrimSpace(stringExt(op.Extensions, "x-silo-raw-reason")) == "" {
			fail("%s: raw protocol requires its exclusion reason", where)
		}
		if method != "get" && method != "head" && method != "post" {
			fail("%s: raw registration supports only GET, HEAD and POST", where)
		}
		if method == "post" && stringExt(op.Extensions, "x-silo-retry-safety") == "" {
			fail("%s: raw POST must declare retry safety", where)
		}
		if _, upgrade := op.Responses["101"]; protocol == "websocket" && (method != "get" || !upgrade) {
			fail("%s: websocket requires GET with an explicit 101 response", where)
		}
		if op.RequestBody != nil || guarded || conditional || createOnly {
			fail("%s: raw handshake advertises unsupported structured controls", where)
		}
		for status, response := range op.Responses {
			code, err := strconv.Atoi(status)
			if err != nil {
				continue
			}
			if code == 101 {
				if method != "get" || protocol != "websocket" || len(response.Content) != 0 {
					fail("%s: 101 requires a bodyless GET websocket handshake", where)
				}
				for _, header := range []string{"Connection", "Upgrade", "Sec-WebSocket-Accept"} {
					if !response.hasHeader(header) {
						fail("%s: websocket response must document header %s", where, header)
					}
				}
			}
			if apiv2.RawSuccessStatus(strings.ToUpper(method), protocol, code) {
				if code >= 300 && !response.hasHeader("Location") {
					fail("%s: redirect response must document Location", where)
				}
				if method != "head" && code >= 200 && code < 300 && code != 204 && len(response.Content) == 0 {
					fail("%s: raw response body requires its media type", where)
				}
				for media := range response.Content {
					media = strings.ToLower(strings.TrimSpace(strings.SplitN(media, ";", 2)[0]))
					if media == "application/json" || strings.HasSuffix(media, "+json") {
						fail("%s: raw handshake cannot replace a structured JSON operation", where)
					}
				}
			}
		}
		implied = apiv2.RawImpliedStatuses(class, serviceBacked)
	}
	for _, status := range implied {
		if _, ok := op.Responses[strconv.Itoa(status)]; !ok {
			fail("%s: status %d is implied by class %s but not documented", where, status, class)
		}
	}
	if _, ok := op.Responses["default"]; ok {
		fail("%s: a default response hides undocumented statuses", where)
	}
}

func lintSecurity(fail func(string, ...any), where string, op operation) {
	class := apiv2.Class(stringExt(op.Extensions, extClass))
	if class == apiv2.ClassPublic {
		if len(op.Security) != 0 {
			fail("%s: a public operation must not declare security", where)
		}
		return
	}
	found := false
	for _, req := range op.Security {
		if _, ok := req[securityScheme]; ok {
			found = true
		}
	}
	if !found {
		fail("%s: class %s requires the %s security scheme", where, class, securityScheme)
	}
}

// lintDeprecation requires `deprecated: true` and x-silo-deprecation to
// travel together, and the extension to carry what the listener emits: an
// RFC 3339 `at`, an https `link` under the documentation origin, and an
// optional RFC 3339 `sunset` no earlier than `at`. A flag without the
// extension documents a retirement the headers do not announce; the
// extension without the flag hides one from generated clients.
func lintDeprecation(fail func(string, ...any), where string, op operation) {
	raw, has := op.Extensions[extDeprecation]
	if !op.Deprecated && !has {
		return
	}
	if op.Deprecated && !has {
		fail("%s: deprecated: true without %s", where, extDeprecation)
		return
	}
	if !op.Deprecated {
		fail("%s: %s without deprecated: true", where, extDeprecation)
	}
	ext, ok := raw.(map[string]any)
	if !ok {
		fail("%s: %s is not an object", where, extDeprecation)
		return
	}
	at, atErr := time.Parse(time.RFC3339, stringExt(ext, deprecationAt))
	if atErr != nil {
		fail("%s: %s.at is not an RFC 3339 instant", where, extDeprecation)
	}
	link := stringExt(ext, deprecationLink)
	if !strings.HasPrefix(link, apiv2.DocsOrigin) {
		fail("%s: %s.link %q is not an https URL under %s", where, extDeprecation, link, apiv2.DocsOrigin)
	}
	if rawSunset, planned := ext[deprecationSunset]; planned {
		sunset, err := time.Parse(time.RFC3339, stringExt(ext, deprecationSunset))
		switch {
		case err != nil:
			fail("%s: %s.sunset %v is not an RFC 3339 instant", where, extDeprecation, rawSunset)
		case atErr == nil && sunset.Before(at):
			fail("%s: %s.sunset precedes at", where, extDeprecation)
		}
	}
	for key := range ext {
		switch key {
		case deprecationAt, deprecationLink, deprecationSunset:
		default:
			fail("%s: %s has unknown member %q", where, extDeprecation, key)
		}
	}
}

// lintNamed requires a request or response schema to be a $ref to a
// top-level schema, an array of such, or a marked extension bag; an inline
// object is an anonymous schema no generated client can name stably.
func lintNamed(fail func(string, ...any), where string, s *schema) {
	if s == nil {
		fail("%s: schema is missing", where)
		return
	}
	if s.Ref != "" {
		return
	}
	if s.Type == "array" {
		lintNamed(fail, where+" items", s.Items)
		return
	}
	if s.Type == typeObject {
		if stringExt(s.Extensions, extExtensionBag) != "" {
			return
		}
		fail("%s: anonymous object schema; name it as a top-level type", where)
		return
	}
	// Scalars are fine inline.
}

// lintFreeForm walks a schema tree and refuses any object that accepts
// undeclared members without the extension-bag marker.
func lintFreeForm(fail func(string, ...any), where string, s *schema) {
	if s == nil || s.Ref != "" {
		return
	}
	if s.Type == typeObject || len(s.Properties) > 0 {
		bag := stringExt(s.Extensions, extExtensionBag) != ""
		switch ap := s.AdditionalProperties.(type) {
		case nil:
			if len(s.Properties) == 0 && !bag {
				fail("%s: object without properties or additionalProperties:false is free-form", where)
			} else if !bag {
				fail("%s: additionalProperties is unset; the generator emits false for a closed object", where)
			}
		case bool:
			if ap && !bag {
				fail("%s: additionalProperties:true without %s", where, extExtensionBag)
			}
		case map[string]any:
			// A typed map (additionalProperties: {schema}) is a declared
			// shape, not a free-form object; its value schema is checked.
			var value schema
			raw, _ := json.Marshal(ap)
			_ = json.Unmarshal(raw, &value)
			lintFreeForm(fail, where+".additionalProperties", &value)
		}
	}
	for _, name := range sortedKeys(s.Properties) {
		lintFreeForm(fail, where+"."+name, s.Properties[name])
	}
	lintFreeForm(fail, where+".items", s.Items)
}

// datePattern is the YYYY-MM-DD shape a date member's pattern carries when it
// cannot use format: date, such as one that also accepts "" to clear.
const datePattern = "[0-9]{4}-[0-9]{2}-[0-9]{2}"

// The member names lintValueShape checks, and what satisfies them.
var (
	dateName        = regexp.MustCompile(`^(date|.*_date)$`)
	urlName         = regexp.MustCompile(`^(url|uri|href|endpoint|issuer|.*_ur[il]s?)$`)
	localeName      = regexp.MustCompile(`language|countr|locale`)
	uriFormats      = map[string]bool{"uri": true, "uri-reference": true}
	localeStandards = regexp.MustCompile(`ISO 639|ISO 3166|BCP 47`)
)

// shapePending lists members whose shape waits on other work, each with the
// issue or pull request it waits on. The lint fails an entry whose member
// already declares its shape, so the entry goes when that work lands.
var shapePending = map[string]string{
	"components.schemas.AdminItemMetadataUpdate.release_date":   pendingItemDates,
	"components.schemas.AdminItemMetadataUpdate.first_air_date": pendingItemDates,
	"components.schemas.AdminItemMetadataUpdate.last_air_date":  pendingItemDates,
	"components.schemas.AdminItemMetadataUpdate.air_date":       pendingItemDates,
	"components.schemas.DeviceLoginStart.verification_uri":      "#2917: built from any client's X-Forwarded-Proto when no public URL is set",
}

const pendingItemDates = "#2810 validates the item metadata dates in the handler"

// notURI is the description phrase a URL-named member carries instead of a
// URI format when the server stores or relays its value without parsing it
// as a URL (admin input, plugin metadata, client input).
const notURI = "not validated as a URI"

// lintMemberShapes walks a schema's members and array items and applies
// lintValueShape to each named member; a $ref is checked as its own
// component.
func lintMemberShapes(fail func(string, ...any), where string, s *schema) {
	if s == nil || s.Ref != "" {
		return
	}
	for _, name := range sortedKeys(s.Properties) {
		member := s.Properties[name]
		lintValueShape(fail, where+"."+name, name, "", member)
		lintMemberShapes(fail, where+"."+name, member)
	}
	lintMemberShapes(fail, where+".items", s.Items)
}

// lintValueShape requires a string member (or a string array's items) whose
// name says what it carries to declare that shape, so generated clients can
// type it: a date declares format date or date-time, or a YYYY-MM-DD
// pattern; a URL declares format uri or uri-reference, or says in its
// description that it is not validated as a URI; a language, country,
// or locale names its standard (ISO 639, ISO 3166, BCP 47) in its
// description.
func lintValueShape(fail func(string, ...any), where, name, description string, s *schema) {
	if s == nil {
		return
	}
	value := s
	if s.Type == "array" && s.Items != nil {
		value = s.Items
	}
	if value.Ref != "" || value.Type != typeString {
		return
	}
	description += " " + s.Description
	if value != s {
		description += " " + value.Description
	}
	var problem string
	switch {
	case dateName.MatchString(name):
		if value.Format != "date" && value.Format != "date-time" && !strings.Contains(value.Pattern, datePattern) {
			problem = "date member declares neither format date nor a YYYY-MM-DD pattern"
		}
	case urlName.MatchString(name):
		if !uriFormats[value.Format] && !strings.Contains(description, notURI) {
			problem = "URL member declares neither format uri nor uri-reference, nor says it is " + notURI
		}
	case localeName.MatchString(name):
		if !localeStandards.MatchString(description) {
			problem = "language or country member does not name its standard (ISO 639, ISO 3166, BCP 47)"
		}
	default:
		return
	}
	if reason, pending := shapePending[where]; pending {
		if problem == "" {
			fail("%s: declares its shape; delete its shapePending entry (%s)", where, reason)
		}
		return
	}
	if problem != "" {
		fail("%s: %s", where, problem)
	}
}

// --- document model (only the members the lint reads) -----------------------

type document struct {
	Paths      map[string]map[string]operation `json:"paths"`
	Components struct {
		Schemas         map[string]*schema         `json:"schemas"`
		SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
	} `json:"components"`
}

type operation struct {
	OperationID string                `json:"operationId"`
	Parameters  []parameter           `json:"parameters"`
	RequestBody *requestBody          `json:"requestBody"`
	Responses   map[string]response   `json:"responses"`
	Security    []map[string][]string `json:"security"`
	Deprecated  bool                  `json:"deprecated"`
	Extensions  map[string]any        `json:"-"`
}

func (o *operation) UnmarshalJSON(b []byte) error {
	type plain operation
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*o = operation(p)
	o.Extensions = extensions(b)
	return nil
}

type parameter struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Schema      *schema `json:"schema"`
}

type requestBody struct {
	Content map[string]mediaType `json:"content"`
}

type response struct {
	Content map[string]mediaType `json:"content"`
	Headers map[string]mediaType `json:"headers"`
}

func (r response) hasHeader(name string) bool {
	for key, header := range r.Headers {
		if strings.EqualFold(key, name) && header.Schema != nil {
			return true
		}
	}
	return false
}

type mediaType struct {
	Schema *schema `json:"schema"`
}

type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 schemaType         `json:"type"`
	Format               string             `json:"format"`
	Pattern              string             `json:"pattern"`
	Description          string             `json:"description"`
	Properties           map[string]*schema `json:"properties"`
	Items                *schema            `json:"items"`
	AdditionalProperties any                `json:"additionalProperties"`
	Extensions           map[string]any     `json:"-"`
}

// schemaType is the JSON Schema `type` member: a single name, or in OpenAPI
// 3.1 a list such as ["string", "null"] for a nullable member. The lint
// reasons about the non-null name.
type schemaType string

func (t *schemaType) UnmarshalJSON(b []byte) error {
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*t = schemaType(single)
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, name := range list {
		if name != "null" {
			*t = schemaType(name)
			return nil
		}
	}
	*t = ""
	return nil
}

func (s *schema) UnmarshalJSON(b []byte) error {
	type plain schema
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = schema(p)
	s.Extensions = extensions(b)
	return nil
}

// extensions collects the x- members of an object.
func extensions(b []byte) map[string]any {
	var all map[string]any
	if err := json.Unmarshal(b, &all); err != nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range all {
		if strings.HasPrefix(k, "x-") {
			out[k] = v
		}
	}
	return out
}

func stringExt(ext map[string]any, key string) string {
	s, _ := ext[key].(string)
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isLowerCamel(s string) bool {
	if s == "" || !unicode.IsLower(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isPascal(s string) bool {
	if s == "" || !unicode.IsUpper(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
