package apiv2

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// permissionMemberName reports whether a member names a permission: allowed,
// or a name ending in _allowed.
func permissionMemberName(name string) bool {
	return name == "allowed" || strings.HasSuffix(name, "_allowed")
}

// schemaPermissionMembers lists the boolean permission members of a schema,
// at any depth, as paths ("allowed", "items[].requests_allowed").
func schemaPermissionMembers(schemas map[string]any, schema any, path string, seen map[string]bool, out *[]string) {
	node, _ := schema.(map[string]any)
	if node == nil {
		return
	}
	if ref, ok := node["$ref"].(string); ok {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if seen[name] {
			return
		}
		next := map[string]bool{name: true}
		for k := range seen {
			next[k] = true
		}
		schemaPermissionMembers(schemas, schemas[name], path, next, out)
		return
	}
	props, _ := node["properties"].(map[string]any)
	for name, prop := range props {
		member := strings.TrimPrefix(path+"."+name, ".")
		resolved, _ := prop.(map[string]any)
		if ref, ok := resolved["$ref"].(string); ok {
			resolved, _ = schemas[ref[strings.LastIndex(ref, "/")+1:]].(map[string]any)
		}
		if permissionMemberName(name) && isBooleanSchema(resolved) {
			*out = append(*out, member)
		}
		schemaPermissionMembers(schemas, prop, member, seen, out)
	}
	if items, ok := node["items"]; ok {
		schemaPermissionMembers(schemas, items, path+"[]", seen, out)
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		subs, _ := node[key].([]any)
		for _, sub := range subs {
			schemaPermissionMembers(schemas, sub, path, seen, out)
		}
	}
}

func isBooleanSchema(s map[string]any) bool {
	switch t := s["type"].(type) {
	case string:
		return t == "boolean"
	case []any:
		return slices.Contains(t, any("boolean"))
	}
	return false
}

// callablePermissionMembers is every permission member of a success response
// of an operation a session another app's key opened may call, keyed as
// capabilityMembers is.
func callablePermissionMembers(t *testing.T) map[string]bool {
	t.Helper()
	doc := generatedDocument(t)
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	found := map[string]bool{}
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if d == nil || d.Verdict == VerdictRefused {
			continue
		}
		responses, _ := op.Raw["responses"].(map[string]any)
		for code, r := range responses {
			if !strings.HasPrefix(code, "2") {
				continue
			}
			content, _ := r.(map[string]any)["content"].(map[string]any)
			for _, media := range content {
				var members []string
				schemaPermissionMembers(schemas, media.(map[string]any)["schema"], "", map[string]bool{}, &members)
				for _, m := range members {
					found[op.ID+" "+m] = true
				}
			}
		}
	}
	return found
}

// TestPermissionMembersStateTheirCase: a capability read never reports as
// allowed an action whose operations are all refused to the session asking.
// Every boolean member named allowed or *_allowed, at any depth, of a success
// response a session another app's key opened may read either governs (names
// the operations it answers for) or is exempt with a reason saying what the
// app would do if it believed it. A member with neither fails here by name,
// as an operation with no decision fails the decisions test; so does a
// governed operation that does not exist, and a table row no response has.
// Catches a new capability read, or a new permission flag, that tells the app
// it may do what the gate refuses without anyone deciding.
func TestPermissionMembersStateTheirCase(t *testing.T) {
	found := callablePermissionMembers(t)
	ids := map[string]bool{}
	for _, op := range documentOperations(t) {
		ids[op.ID] = true
	}
	for key := range found {
		entry, ok := capabilityMembers[key]
		switch {
		case !ok:
			t.Errorf("%s: a permission member with no row in capabilityMembers: govern it or exempt it with a reason", key)
		case len(entry.governs) == 0 && strings.TrimSpace(entry.exempt) == "":
			t.Errorf("%s: neither governs nor is exempt", key)
		case len(entry.governs) > 0 && entry.exempt != "":
			t.Errorf("%s: both governs and is exempt", key)
		}
		for _, id := range entry.governs {
			if !ids[id] {
				t.Errorf("%s governs %s, which is not an operation", key, id)
			}
		}
	}
	for key := range capabilityMembers {
		if !found[key] {
			t.Errorf("%s: no response such a session may read has this member; remove the row", key)
		}
	}
}

// answerPermissionMembers lists the permission members a JSON answer sets
// true, as paths keyed like capabilityMembers ("items[].requests_allowed").
func answerPermissionMembers(v any, path string, out map[string]bool) {
	switch node := v.(type) {
	case map[string]any:
		for name, value := range node {
			member := strings.TrimPrefix(path+"."+name, ".")
			if b, ok := value.(bool); ok && b && permissionMemberName(name) {
				out[member] = true
			}
			answerPermissionMembers(value, member, out)
		}
	case []any:
		for _, item := range node {
			answerPermissionMembers(item, path+"[]", out)
		}
	}
}

// checkLivePermissionMembers is the live half of the rule: for every allowed
// GET such a session calls through h, a permission member the answer sets
// true has a row in capabilityMembers, and a governed one is not true when
// every operation it names is refused to that session.
func checkLivePermissionMembers(t *testing.T, h http.Handler, token string) (checked int) {
	t.Helper()
	ops := documentOperations(t)
	refused := map[string]bool{}
	for _, op := range ops {
		if d := documentedDecision(t, op); d != nil && d.Verdict == VerdictRefused {
			refused[op.ID] = true
		}
	}
	for _, op := range ops {
		d := documentedDecision(t, op)
		if op.Method != http.MethodGet || !judgedClass(op.Class) || d == nil || d.Verdict == VerdictRefused {
			continue
		}
		rec := do(t, h, op.Method, pathParam.ReplaceAllString(op.Path, "1"), "", with(bearer(token), "X-Profile-Id", "p-owner"))
		var body any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			continue
		}
		checked++
		trues := map[string]bool{}
		answerPermissionMembers(body, "", trues)
		for member := range trues {
			key := op.ID + " " + member
			entry, ok := capabilityMembers[key]
			if !ok {
				t.Errorf("%s answers %s true to another app's session and has no row", op.ID, member)
				continue
			}
			if len(entry.governs) > 0 && !slices.ContainsFunc(entry.governs, func(id string) bool { return !refused[id] }) {
				t.Errorf("%s answers %s true, but every operation it governs is refused to another app's session", op.ID, member)
			}
		}
	}
	return checked
}

// TestAllowedReadsTellGrantedAppsNoRefusedAction: the live check over the
// fakes the capability tests wire, so the services report available.
func TestAllowedReadsTellGrantedAppsNoRefusedAction(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Accounts = passwordChanger{deps.Accounts.(fakeAccounts)}
	wireCapabilityServices(&deps)
	if checked := checkLivePermissionMembers(t, newTestHandler(t, deps), grantedAppToken); checked == 0 {
		t.Fatal("no allowed GET answered 200")
	}
}
