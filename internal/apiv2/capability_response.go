package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// CapabilityInput binds both RFC 9110 read preconditions. Authorization and
// domain configuration are evaluated before considering the cached document.
type CapabilityInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

func (c *Capability) commonCapability() *Capability { return c }

func configuredCapabilityState(configured bool) string {
	if configured {
		return StateAvailable
	}
	return StateNotConfigured
}
func enabledCapabilityState(enabled bool) string {
	if enabled {
		return StateAvailable
	}
	return StateDisabled
}
func configuredEnabledCapabilityState(configured, enabled bool) string {
	if !configured {
		return StateNotConfigured
	}
	return enabledCapabilityState(enabled)
}

type capabilityDocument interface{ commonCapability() *Capability }

// prepareCapabilityOperation recognizes the typed common head, including
// anonymous response bodies, without inferring domain semantics from JSON
// property names or operation IDs. Domain handlers own support/configuration;
// this registration adapter owns representation identity and revalidation.
func prepareCapabilityOperation[I, O any](op *Operation, handler func(context.Context, *I) (*O, error)) func(context.Context, *I) (*O, error) {
	output := reflect.TypeFor[O]()
	if output.Kind() != reflect.Struct {
		return handler
	}
	body, ok := output.FieldByName("Body")
	if !ok || !reflect.PointerTo(body.Type).Implements(reflect.TypeFor[capabilityDocument]()) {
		return handler
	}
	if op.Method != http.MethodGet {
		panic("apiv2: capability documents must be GET operations")
	}
	op.Conditional = true
	if err := checkConcurrencyShape(*op, reflect.TypeFor[I](), output); err != nil {
		panic(fmt.Sprintf("apiv2: %s: %v", op.OperationID, err))
	}
	etagField := capabilityHeaderField(output, "ETag", op.OperationID)
	cacheField := capabilityHeaderField(output, "Cache-Control", op.OperationID)
	matchField := capabilityHeaderField(reflect.TypeFor[I](), "If-Match", op.OperationID)
	noneField := capabilityHeaderField(reflect.TypeFor[I](), "If-None-Match", op.OperationID)
	operationID, scoped := op.OperationID, op.Class != ClassPublic
	return func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		if err != nil || out == nil {
			return out, err
		}
		v := reflect.ValueOf(out).Elem()
		document, ok := v.FieldByIndex(body.Index).Addr().Interface().(capabilityDocument)
		if !ok {
			return nil, NewProblem(TypeInternalError, "The capability document has an invalid type.")
		}
		head := document.commonCapability()
		if head.State == "" {
			if domain, ok := document.(interface{ capabilityState() string }); ok {
				head.State = domain.capabilityState()
			}
		}
		switch head.State {
		case StateAvailable, StateDisabled, StateNotConfigured, StateUnsupported:
		default:
			return nil, NewProblem(TypeInternalError, "The capability document has no valid support state.")
		}
		if scoped {
			allowed := head.State == StateAvailable && (head.Allowed == nil || *head.Allowed)
			head.Allowed = &allowed
		}
		// Hash the complete authorized representation with the self-referential
		// revision cleared. Domain fields (including configuration limits) and the
		// effective permission answer all participate; service health is not state.
		head.Revision = ""
		data, err := json.Marshal(v.FieldByIndex(body.Index).Interface())
		if err != nil {
			return nil, NewProblem(TypeInternalError, "The capability document could not be encoded.")
		}
		digest := sha256.Sum256(append([]byte(operationID+"\x00"), data...))
		head.Revision = hex.EncodeToString(digest[:])
		// The entity tag describes the final representation, including its revision.
		data, err = json.Marshal(v.FieldByIndex(body.Index).Interface())
		if err != nil {
			return nil, NewProblem(TypeInternalError, "The capability document could not be encoded.")
		}
		digest = sha256.Sum256(append([]byte(operationID+"\x00"), data...))
		tag := EntityTag{Opaque: hex.EncodeToString(digest[:])}
		v.FieldByIndex(etagField).SetString(tag.String())
		v.FieldByIndex(cacheField).SetString(cachePrivateNoCache)
		input := reflect.ValueOf(in).Elem()
		match, none := input.FieldByIndex(matchField), input.FieldByIndex(noneField)
		unchanged, problem := EvaluateReadPreconditions(match.String(), none.String(), tag)
		if problem != nil {
			return nil, problem
		}
		if unchanged {
			return NotModified(out, tag), nil
		}
		return out, nil
	}
}

// The socket and Apple display-token operations require a bounded login session;
// ordinary API-key authorization does not grant these delegated credentials.
func capabilityLoginAllowed(ctx context.Context) bool {
	claims := claimsFrom(ctx)
	return claims != nil && claims.TokenType == auth.TokenTypeAccess && claims.SessionID != "" && claims.ExpiresAt != nil && claims.ExpiresAt.After(time.Now())
}

// Huma reads direct header fields. Validate and capture them once while routes
// are registered so malformed capability declarations cannot panic a request.
func capabilityHeaderField(t reflect.Type, header, operation string) []int {
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.IsExported() && !field.Anonymous && field.Type.Kind() == reflect.String && strings.EqualFold(field.Tag.Get("header"), header) {
				return field.Index
			}
		}
	}
	panic(fmt.Sprintf("apiv2: %s: capability requires a direct string %s header", operation, header))
}

// documentCapabilityScope refines the shared Go head using every registered
// use of the response schema. A public use wins permanently, regardless of
// registration order; schemas used only by scoped operations require allowed.
func documentCapabilityScope(reg *Registry, op Operation, output reflect.Type) {
	if output.Kind() != reflect.Struct {
		return
	}
	body, ok := output.FieldByName("Body")
	if !ok || !reflect.PointerTo(body.Type).Implements(reflect.TypeFor[capabilityDocument]()) {
		return
	}
	response := reg.api.OpenAPI().Paths[op.Path].Get.Responses["200"]
	ref := response.Content[mediaTypeJSON].Schema.Ref
	schema := reg.api.OpenAPI().Components.Schemas.Map()[strings.TrimPrefix(ref, "#/components/schemas/")]
	if schema == nil {
		panic(fmt.Sprintf("apiv2: %s: capability response has no registered body schema", op.OperationID))
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.publicCapabilitySchemas == nil {
		reg.publicCapabilitySchemas = make(map[string]bool)
	}
	public := reg.publicCapabilitySchemas[ref] || op.Class == ClassPublic
	reg.publicCapabilitySchemas[ref] = public
	if public {
		schema.Required = slices.DeleteFunc(schema.Required, func(field string) bool { return field == "allowed" })
	} else if !slices.Contains(schema.Required, "allowed") {
		schema.Required = append(schema.Required, "allowed")
	}
}
