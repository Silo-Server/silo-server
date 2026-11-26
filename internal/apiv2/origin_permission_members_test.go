package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// permissionMemberName reports whether a member names a permission: allowed,
// a name ending in _allowed, or one starting can_ or self_can_.
func permissionMemberName(name string) bool {
	return name == "allowed" || strings.HasSuffix(name, "_allowed") ||
		strings.HasPrefix(name, "can_") || strings.HasPrefix(name, "self_can_")
}

// schemaMembers lists every member of a schema, at any depth, as paths
// ("allowed", "items[].requests_allowed"), each marked true when it is a
// boolean permission member (permissionMemberName).
func schemaMembers(schemas map[string]any, schema any, path string, seen map[string]bool, out map[string]bool) {
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
		schemaMembers(schemas, schemas[name], path, next, out)
		return
	}
	props, _ := node["properties"].(map[string]any)
	for name, prop := range props {
		member := strings.TrimPrefix(path+"."+name, ".")
		resolved, _ := prop.(map[string]any)
		if ref, ok := resolved["$ref"].(string); ok {
			resolved, _ = schemas[ref[strings.LastIndex(ref, "/")+1:]].(map[string]any)
		}
		out[member] = out[member] || permissionMemberName(name) && isBooleanSchema(resolved)
		schemaMembers(schemas, prop, member, seen, out)
	}
	if items, ok := node["items"]; ok {
		schemaMembers(schemas, items, path+"[]", seen, out)
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		subs, _ := node[key].([]any)
		for _, sub := range subs {
			schemaMembers(schemas, sub, path, seen, out)
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

// callableMembers is every member of a success response of an operation a
// session another app's key opened may call, keyed as capabilityMembers is,
// each marked true when it is a boolean permission member.
func callableMembers(t *testing.T) map[string]bool {
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
				members := map[string]bool{}
				schemaMembers(schemas, media.(map[string]any)["schema"], "", map[string]bool{}, members)
				for m, permission := range members {
					found[op.ID+" "+m] = found[op.ID+" "+m] || permission
				}
			}
		}
	}
	return found
}

// TestPermissionMembersStateTheirCase: a capability read never reports as
// allowed an action whose operations are all refused to the session asking.
// Every boolean permission member (permissionMemberName), at any depth, of a
// success response a session another app's key opened may read, and every
// member a row names under another name, either governs (names the
// operations it answers for) or is exempt with a reason saying what the app
// would do if it believed it. A member with neither fails here by name, as an
// operation with no decision fails the decisions test; so does a governed
// operation that does not exist, and a table row no response has. Catches a
// new capability read, or a new permission flag, that tells the app it may do
// what the gate refuses without anyone deciding.
func TestPermissionMembersStateTheirCase(t *testing.T) {
	found := callableMembers(t)
	ids := map[string]bool{}
	for _, op := range documentOperations(t) {
		ids[op.ID] = true
	}
	for key, permission := range found {
		entry, ok := capabilityMembers[key]
		switch {
		case !ok && !permission:
			continue
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
		if _, ok := found[key]; !ok {
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
	h := newTestHandler(t, deps)
	if checked := checkLivePermissionMembers(t, h, grantedAppToken); checked == 0 {
		t.Fatal("no allowed GET answered 200")
	}
	// The governed members the live check exercises: each is true for the
	// person's own session here, so only the rule makes it false for
	// another app's, and dropping its gate fails above by name.
	own := map[string]bool{}
	for _, op := range documentOperations(t) {
		if op.Method != http.MethodGet || !judgedClass(op.Class) {
			continue
		}
		rec := do(t, h, op.Method, pathParam.ReplaceAllString(op.Path, "1"), "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
		if rec.Code == http.StatusInternalServerError {
			// A fake missing a method panics into a 500 and would hide here.
			// A 503 is a service this fixture leaves unwired.
			t.Errorf("%s answered the person's own session %d: %s", op.ID, rec.Code, rec.Body.String())
			continue
		}
		var body any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			continue
		}
		trues := map[string]bool{}
		answerPermissionMembers(body, "", trues)
		for member := range trues {
			own[op.ID+" "+member] = true
		}
	}
	for _, key := range []string{
		"getAccountPasswordCapability allowed", "getNotificationEmailVerificationCapabilities allowed", "getProgressBootstrapCapabilities allowed",
		"getDownloadCapability allowed", "getDownloadCapability download_allowed", "getDownloadCapability transcode_user_allowed",
		"getCurrentUser download_allowed", "getSubtitleSyncStatus allowed", "getSubtitleAIStatus allowed", "getSubtitleProviderStatus allowed",
		"listProfiles items[].requests_allowed",
	} {
		if !own[key] {
			t.Errorf("%s is not true for the person's own session, so the live check cannot see its gate", key)
		}
	}
}

// The services below report themselves available and answer only what a
// capability read asks, so the person's own session is told allowed and the
// rule alone tells another app's session not.
type availableDiagnostics struct{ DiagnosticsIngressService }

func (availableDiagnostics) UploadStatus(context.Context, int) (diagnostics.Status, error) {
	return diagnostics.Status{Status: diagnostics.StatusAvailable, AcceptedSchemaVersions: []int{1}, AcceptedCrashSources: []string{}}, nil
}

type availableProfileTransfer struct{ ProfileTransferService }

func (availableProfileTransfer) Capability() backup.ProfileExportCapability {
	return backup.ProfileExportCapability{Format: "silo-profile-export", Version: 1, DataTypes: []string{}}
}

type availableTitleAllowances struct{ TitleAllowanceService }

func (availableTitleAllowances) Supported(context.Context, int) bool { return true }

type availableTrailers struct{ CatalogTrailerService }

func (availableTrailers) TrailerRefreshCapability() handlers.TrailerRefreshCapabilityView {
	return handlers.TrailerRefreshCapabilityView{Enabled: true, Statuses: []string{}, SupportedTypes: []string{}}
}

type availableMetadataAI struct{ MetadataAIService }

func (availableMetadataAI) Status(context.Context) handlers.MetadataAIStatusView {
	return handlers.MetadataAIStatusView{Enabled: true, OnView: metadataAIOnViewOff}
}

// TestRefusedCapabilitiesTellAnotherAppNotAllowed covers the governed
// capability members the live check cannot reach: each answers for
// operations all refused to a session another app's key opened, but the live
// check's fixture leaves its service unwired, so the person's own session is
// not told allowed there either. Each row wires the smallest fake that makes
// the read available, and the person's own session is told allowed while
// another app's session is told not. The four room members
// (self_can_manage_room, self_can_control_transport on the room read and
// join) are covered by TestRoomReadLeavesOutAnotherDevicesStream. Every other
// governed member names an operation open to such a session, so the rule
// never makes it false and the live check has nothing to see. Catches the
// rule dropping off one of these reads.
func TestRefusedCapabilitiesTellAnotherAppNotAllowed(t *testing.T) {
	for _, tc := range []struct {
		path string
		wire func(*Dependencies)
	}{
		{"/devices/push/apple/capabilities", func(d *Dependencies) { d.OrderedApplePush = new(fakeOrderedApple) }},
		{"/notifications/push/devices/capabilities", func(d *Dependencies) { d.OrderedAndroidPush = new(fakeOrderedPush) }},
		{"/diagnostics/capabilities", func(d *Dependencies) { d.DiagnosticsIngress = availableDiagnostics{} }},
		{"/history-imports/capability", func(d *Dependencies) { d.HistoryImports = struct{ HistoryImportService }{} }},
		{"/onboarding/capabilities", func(d *Dependencies) { d.Onboarding = struct{ OnboardingService }{} }},
		{"/capabilities/profile-export", func(d *Dependencies) { d.ProfileTransfer = availableProfileTransfer{} }},
		{"/capabilities/title-allowances", func(d *Dependencies) { d.TitleAllowances = availableTitleAllowances{} }},
		{"/capabilities/trailers", func(d *Dependencies) { d.CatalogTrailers = availableTrailers{} }},
		{"/capabilities/metadata-ai", func(d *Dependencies) { d.MetadataAI = availableMetadataAI{} }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			tc.wire(&deps)
			h := newTestHandler(t, deps)
			if own, ok := capabilityAllowed(t, h, Prefix+tc.path, liveMemberToken); !ok || !own {
				t.Fatalf("the person's own session was not told allowed (ok %v)", ok)
			}
			if app, ok := capabilityAllowed(t, h, Prefix+tc.path, liveGrantedAppToken); !ok || app {
				t.Fatalf("another app's session told allowed=%v (ok %v)", app, ok)
			}
		})
	}
}

// configuredMDBList reports an MDBList key, so mdblist_search is offered.
type configuredMDBList struct{ CollectionImportService }

func (configuredMDBList) MDBListConfigured() bool { return true }

// truthy reports whether a JSON value offers something: true, a non-empty
// list, string or object, or a nonzero number.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	case float64:
		return x != 0
	case map[string]any:
		return len(x) > 0
	}
	return false
}

// powerListName reports whether a list member names powers: permissions, or
// a name ending in _permissions, _actions or _capabilities.
func powerListName(name string) bool {
	return name == "permissions" || strings.HasSuffix(name, "_permissions") ||
		strings.HasSuffix(name, "_actions") || strings.HasSuffix(name, "_capabilities")
}

// isArraySchema reports whether a schema is an array: type "array", or a
// type list that holds it (a nullable list, ["array","null"]).
func isArraySchema(s map[string]any) bool {
	switch t := s["type"].(type) {
	case string:
		return t == "array"
	case []any:
		return slices.Contains(t, any("array"))
	}
	return false
}

// TestCapabilityReadsAnswerForTheSession: the collection and Watch Together
// capability reads answer, member by member, for the session asking. With
// every feature on, the person's own session reads each governed member as
// offered (the server's answer, as before), and another app's session reads
// a member whose operations are all refused to it as false or empty and
// every other member as the person's own session does. Catches a member that
// offers such a session a collection write or a host power the gate refuses,
// and a member the rule wrongly takes from the person's own session.
func TestCapabilityReadsAnswerForTheSession(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.PersonalCollections = &fakePersonalCollections{collages: true,
		features: userstore.CollectionFeatures{Imports: true, Artwork: true, ItemReorder: true, Description: true}}
	deps.CollectionImports = configuredMDBList{}
	deps.WatchTogetherCapability = &fakeWatchTogetherCapability{available: true}
	wireWatchTogetherCapabilityFakes(&deps)
	h := newTestHandler(t, deps)
	refused := map[string]bool{}
	for _, op := range documentOperations(t) {
		if d := documentedDecision(t, op); d != nil && d.Verdict == VerdictRefused {
			refused[op.ID] = true
		}
	}
	for op, path := range map[string]string{"getCollectionCapabilities": "/collections/capabilities", "getWatchTogetherCapabilities": "/watch-together/capabilities"} {
		read := func(token string) map[string]any {
			rec := do(t, h, http.MethodGet, Prefix+path, "", with(bearer(token), "X-Profile-Id", "p-owner"))
			var body map[string]any
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
				t.Fatalf("%s %s: %d %s", op, token, rec.Code, rec.Body.String())
			}
			return body
		}
		own, app := read(liveMemberToken), read(liveGrantedAppToken)
		// A member with a row must be in both answers, not null, as the same
		// kind of JSON value, before its value means anything: a renamed,
		// dropped or nulled member fails here.
		for key := range capabilityMembers {
			member, ok := strings.CutPrefix(key, op+" ")
			if !ok {
				continue
			}
			if o, a := own[member], app[member]; o == nil || a == nil || reflect.TypeOf(o) != reflect.TypeOf(a) {
				t.Errorf("%s %s: the person's own answer %v (%T), another app's %v (%T)", op, member, o, o, a, a)
			}
		}
		for member := range own {
			if member == "revision" || member == "state" {
				continue
			}
			entry, ok := capabilityMembers[op+" "+member]
			if !ok {
				t.Errorf("%s %s has no row in capabilityMembers", op, member)
				continue
			}
			o, a := own[member], app[member]
			allRefused := len(entry.governs) > 0 && !slices.ContainsFunc(entry.governs, func(id string) bool { return !refused[id] })
			_, boolean := o.(bool)
			switch {
			case len(entry.governs) > 0 && boolean && o != true:
				t.Errorf("%s %s: the person's own session reads %v, want exactly true with every feature on", op, member, o)
			case len(entry.governs) > 0 && !truthy(o):
				t.Errorf("%s %s: the person's own session reads %v with every feature on", op, member, o)
			case allRefused && boolean && a != false:
				t.Errorf("%s %s: another app's session reads %v, want exactly false: every operation it governs is refused to it", op, member, a)
			case allRefused && truthy(a):
				t.Errorf("%s %s: another app's session reads %v, but every operation it governs is refused to it", op, member, a)
			case !allRefused && !reflect.DeepEqual(a, o):
				t.Errorf("%s %s: another app's session reads %v, the person's own %v", op, member, a, o)
			}
		}
	}
}

// schemaPowerLists lists the array members of a schema whose name names
// powers, at any depth, as paths keyed like capabilityMembers.
func schemaPowerLists(schemas map[string]any, schema any, path string, seen map[string]bool, out *[]string) {
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
		schemaPowerLists(schemas, schemas[name], path, next, out)
		return
	}
	props, _ := node["properties"].(map[string]any)
	for name, prop := range props {
		member := strings.TrimPrefix(path+"."+name, ".")
		resolved, _ := prop.(map[string]any)
		if ref, ok := resolved["$ref"].(string); ok {
			resolved, _ = schemas[ref[strings.LastIndex(ref, "/")+1:]].(map[string]any)
		}
		if isArraySchema(resolved) && powerListName(name) {
			*out = append(*out, member)
		}
		schemaPowerLists(schemas, prop, member, seen, out)
	}
	if items, ok := node["items"]; ok {
		schemaPowerLists(schemas, items, path+"[]", seen, out)
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		subs, _ := node[key].([]any)
		for _, sub := range subs {
			schemaPowerLists(schemas, sub, path, seen, out)
		}
	}
}

// TestPowerListsStateTheirCase: a read never tells a session another app's
// key opened that it holds a power whose operations are all refused to it,
// in a list any more than in a boolean. Every list member named like a list
// of powers in a success response such a session may read has a row saying
// how it is kept to that session; a row no response has fails too. Catches a
// new list of permissions reaching such a session with nobody deciding.
func TestPowerListsStateTheirCase(t *testing.T) {
	doc := generatedDocument(t)
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	found := map[string]bool{}
	for _, op := range documentOperations(t) {
		if d := documentedDecision(t, op); d == nil || d.Verdict == VerdictRefused {
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
				schemaPowerLists(schemas, media.(map[string]any)["schema"], "", map[string]bool{}, &members)
				for _, m := range members {
					found[op.ID+" "+m] = true
				}
			}
		}
	}
	for key := range found {
		if strings.TrimSpace(powerListMembers[key]) == "" {
			t.Errorf("%s: a list of powers with no row in powerListMembers", key)
		}
	}
	for key := range powerListMembers {
		if !found[key] {
			t.Errorf("%s: no response such a session may read has this list; remove the row", key)
		}
	}
}

// TestPowerListScanSeesEveryArrayForm: the scan finds a list of powers
// written inline, as a reference to an array schema, and as a nullable list
// (a type list), the three forms the published document uses. Catches the
// scan missing a list for the way it is written.
func TestPowerListScanSeesEveryArrayForm(t *testing.T) {
	schemas := map[string]any{
		"Powers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"Answer": map[string]any{"properties": map[string]any{
			"inline_permissions":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"ref_permissions":      map[string]any{"$ref": "#/components/schemas/Powers"},
			"nullable_permissions": map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "string"}},
			"names":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}},
	}
	var found []string
	schemaPowerLists(schemas, map[string]any{"$ref": "#/components/schemas/Answer"}, "", map[string]bool{}, &found)
	slices.Sort(found)
	if want := []string{"inline_permissions", "nullable_permissions", "ref_permissions"}; !slices.Equal(found, want) {
		t.Fatalf("found %v, want %v", found, want)
	}
}
