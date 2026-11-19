package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// fakePluginDestinations answers each call with err, after running a
// guarded write's check against revision 7.
type fakePluginDestinations struct {
	err   error
	calls int
}

func (f *fakePluginDestinations) destination() notifications.PluginDestination {
	return notifications.PluginDestination{ID: "dest", ProfileID: "p-owner", PluginID: "example.ntfy", CapabilityID: "ntfy", Name: "Phone", Revision: 7, Enabled: true, CreatedAt: fixedTime()}
}

func (f *fakePluginDestinations) Available() bool { return true }
func (f *fakePluginDestinations) Channels(context.Context) ([]notifications.PluginChannel, error) {
	return []notifications.PluginChannel{{
		PluginChannelRef: notifications.PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"},
		DisplayName:      "ntfy",
		Schema: &pluginv1.ConfigSchema{
			JsonSchema: `{"type":"object","required":["topic"],"properties":{"topic":{"type":"string","maxLength":32,"pattern":"^[a-z]+$"},"token":{"type":"string"},"priority":{"type":"integer","minimum":1,"maximum":5,"default":3}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
				{Key: "topic", Label: "Topic", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
				{Key: "token", Label: "Token", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD, Secret: true},
				{Key: "priority", Label: "Priority", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT, DefaultValue: structpb.NewNumberValue(3),
					Options: []*pluginv1.AdminFormOption{{Value: "1", Label: "Low"}, {Value: "5", Label: "High"}}},
			}},
		},
		PublicFields: []string{"priority", "topic"},
		SecretFields: []string{"token"},
	}}, nil
}
func (f *fakePluginDestinations) ListPage(context.Context, int, string, int, *notifications.Cursor) ([]notifications.PluginDestinationView, error) {
	return []notifications.PluginDestinationView{{PluginDestination: f.destination()}}, nil
}
func (f *fakePluginDestinations) View(_ context.Context, d notifications.PluginDestination) notifications.PluginDestinationView {
	return notifications.PluginDestinationView{PluginDestination: d}
}
func (f *fakePluginDestinations) Create(context.Context, int, string, notifications.PluginDestinationInput) (*notifications.PluginDestination, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	d := f.destination()
	return &d, nil
}
func (f *fakePluginDestinations) UpdateGuarded(_ context.Context, _ int, _, _ string, _ notifications.PluginDestinationInput, check func(int64) error) (*notifications.PluginDestination, error) {
	f.calls++
	if err := check(7); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	d := f.destination()
	d.Revision = 8
	return &d, nil
}
func (f *fakePluginDestinations) DeleteGuarded(_ context.Context, _ int, _, _ string, check func(int64) error) error {
	f.calls++
	if err := check(7); err != nil {
		return err
	}
	return f.err
}
func (f *fakePluginDestinations) Test(context.Context, int, string, string) (*notifications.PluginDestinationTestResult, error) {
	f.calls++
	return &notifications.PluginDestinationTestResult{OK: false, Message: "busy"}, f.err
}

// The plugin destination routes map the service's errors onto statuses
// that keep their meaning: a channel no enabled plugin provides is a 422 on
// create and a 409 on update, and only a plugin that fails to answer is a
// retryable 503.
func TestNotificationPluginDestinationRoutes(t *testing.T) {
	const base = Prefix + "/notifications/plugin-destinations"
	fake := &fakePluginDestinations{}
	deps := parityDeps(false)
	deps.NotificationPluginDestinations = fake
	h := newTestHandler(t, deps)
	owner := profileOwner()
	current := with(owner, "If-Match", notificationPluginDestinationTag("p-owner", "dest", 7).String())
	stale := with(owner, "If-Match", notificationPluginDestinationTag("p-owner", "dest", 6).String())
	create := `{"plugin_id":"example.ntfy","capability_id":"ntfy","name":"Phone","settings":{"topic":"alerts"}}`
	notProvided := fmt.Errorf("%w: %w", notifications.ErrPluginChannelUnavailable, notifications.ErrPluginChannelNotProvided)

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		err     error
		want    ProblemType
		at      string
	}{
		{name: "create on a channel no plugin provides", method: http.MethodPost, path: base, body: create, headers: owner, err: notProvided, want: TypeValidationFailed, at: "body.capability_id"},
		{name: "create while the plugin doesn't answer", method: http.MethodPost, path: base, body: create, headers: owner, err: fmt.Errorf("%w: timeout", notifications.ErrPluginChannelUnavailable), want: TypeDependencyUnavailable},
		{name: "create with a setting out of range", method: http.MethodPost, path: base, body: create, headers: owner,
			err: &notifications.PluginDestinationSettingsError{Fields: []notifications.PluginSettingError{{Field: "topic", Code: "out_of_range", Message: "Out of the allowed range."}}}, want: TypeValidationFailed, at: "body.settings.topic"},
		{name: "create with a name the profile already uses", method: http.MethodPost, path: base, body: create, headers: owner,
			err: fmt.Errorf("%w: %w", notifications.ErrPluginDestinationInvalid, notifications.ErrPluginDestinationNameTaken), want: TypeValidationFailed, at: "body.name"},
		{name: "create refused by the plugin", method: http.MethodPost, path: base, body: create, headers: owner, err: &notifications.PluginDestinationRejectedError{Message: "unknown topic"}, want: TypeValidationFailed, at: "body.settings"},
		{name: "update without If-Match", method: http.MethodPut, path: base + "/dest", body: `{"name":"Tablet"}`, headers: owner, want: TypePreconditionRequired},
		{name: "update with a stale validator", method: http.MethodPut, path: base + "/dest", body: `{"name":"Tablet"}`, headers: stale, want: TypePreconditionFailed},
		{name: "update after the plugin was removed", method: http.MethodPut, path: base + "/dest", body: `{"settings":{"topic":"news"}}`, headers: current, err: notProvided, want: TypeConflict},
		{name: "update of a destination this server can't read", method: http.MethodPut, path: base + "/dest", body: `{"settings":{"topic":"news"}}`, headers: current, err: notifications.ErrPluginDestinationSettingsUnreadable, want: TypeConflict},
		{name: "update of another profile's destination", method: http.MethodPut, path: base + "/dest", body: `{"name":"Tablet"}`, headers: current, err: notifications.ErrPluginDestinationNotFound, want: TypeNotFound},
		{name: "delete without If-Match", method: http.MethodDelete, path: base + "/dest", headers: owner, want: TypePreconditionRequired},
		{name: "test of a missing destination", method: http.MethodPost, path: base + "/dest/test", headers: owner, err: notifications.ErrPluginDestinationNotFound, want: TypeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake.err = tc.err
			p := requireProblem(t, do(t, h, tc.method, tc.path, tc.body, tc.headers), tc.want)
			if tc.at != "" && (len(p.Errors) != 1 || p.Errors[0].Location != tc.at) {
				t.Fatalf("errors = %+v, want one at %s", p.Errors, tc.at)
			}
		})
	}
	fake.err = nil

	// The 503 says when to try again; the permanent statuses don't.
	fake.err = fmt.Errorf("%w: timeout", notifications.ErrPluginChannelUnavailable)
	if rec := do(t, h, http.MethodPost, base, create, owner); rec.Header().Get("Retry-After") == "" {
		t.Fatalf("503 without Retry-After: %v", rec.Header())
	}
	fake.err = nil

	rec := do(t, h, http.MethodPost, base, create, owner)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") == "" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, base+"/dest", `{"name":"Tablet"}`, current); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, base+"/dest", "", current); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, base+"/dest/test", "", owner); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}

}

// The channel list carries the flat form, not the admin schema.
func TestNotificationPluginChannelsListFlatForm(t *testing.T) {
	deps := parityDeps(false)
	deps.NotificationPluginDestinations = &fakePluginDestinations{}
	h := newTestHandler(t, deps)
	owner := profileOwner()
	rec := do(t, h, http.MethodGet, Prefix+"/notifications/plugin-channels", "", owner)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "json_schema") || strings.Contains(rec.Body.String(), "admin_form") {
		t.Fatalf("channels = %d %s", rec.Code, rec.Body.String())
	}
	var channels struct {
		Items []NotificationPluginChannel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil || len(channels.Items) != 1 {
		t.Fatalf("channels body %s: %v", rec.Body.String(), err)
	}
	fields := channels.Items[0].SettingsForm.Fields
	if len(fields) != 3 || fields[0].Key != "topic" || !fields[0].Required || fields[0].Type != "string" || *fields[0].MaxLength != 32 || fields[0].Pattern != "^[a-z]+$" ||
		fields[1].Control != "password" || !fields[1].Secret ||
		fields[2].Type != "integer" || fields[2].Control != "select" || string(fields[2].DefaultValue) != "3" || *fields[2].Maximum != 5 || len(fields[2].Options) != 2 {
		t.Fatalf("form fields = %+v", fields)
	}
	// An optional member a field doesn't have is absent, never null or
	// empty, and an option's value has the field's type.
	var raw struct {
		Items []struct {
			SettingsForm struct {
				Fields []map[string]json.RawMessage `json:"fields"`
			} `json:"settings_form"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	token := raw.Items[0].SettingsForm.Fields[1]
	for _, member := range []string{"description", "placeholder", "pattern", "default_value", "minimum", "maximum", "min_length", "max_length", "options"} {
		if _, present := token[member]; present {
			t.Errorf("token field carries %s = %s, want it absent", member, token[member])
		}
	}
	if options := string(raw.Items[0].SettingsForm.Fields[2]["options"]); options != `[{"value":1,"label":"Low"},{"value":5,"label":"High"}]` {
		t.Errorf("priority options = %s, want integer values", options)
	}

}

// Demo mode refuses every write before it reaches the service.
func TestNotificationPluginDestinationWritesInDemoMode(t *testing.T) {
	const base = Prefix + "/notifications/plugin-destinations"
	current := with(profileOwner(), "If-Match", notificationPluginDestinationTag("p-owner", "dest", 7).String())
	create := `{"plugin_id":"example.ntfy","capability_id":"ntfy","name":"Phone","settings":{"topic":"alerts"}}`
	demo := parityDeps(true)
	demoFake := &fakePluginDestinations{}
	demo.NotificationPluginDestinations = demoFake
	dh := newTestHandler(t, demo)
	for _, write := range []struct{ method, path, body string }{
		{http.MethodPost, base, create},
		{http.MethodPut, base + "/dest", `{"name":"Tablet"}`},
		{http.MethodDelete, base + "/dest", ""},
		{http.MethodPost, base + "/dest/test", ""},
	} {
		requireProblem(t, do(t, dh, write.method, write.path, write.body, current), TypePermissionDenied)
	}
	if demoFake.calls != 0 {
		t.Fatalf("demo mode reached the service %d times", demoFake.calls)
	}
}
