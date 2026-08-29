package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// The two override fields are the whole point of per-node acceleration policy,
// so setting one, reading it back through an ordinary List/Get, and clearing it
// again all have to survive the round trip through Postgres.
func TestRepositoryUpdateHWOverridesRoundTrip(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	node := createTestNode(t, repo, "hw-override")

	if node.HWAccelOverride != nil || node.HWDeviceOverride != nil {
		t.Fatalf("new node already carries overrides: %+v", node)
	}

	accel, device := "vaapi", "/dev/dri/renderD129"
	updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{
		HWAccelOverride:  &accel,
		HWDeviceOverride: &device,
	})
	if err != nil {
		t.Fatalf("set overrides: %v", err)
	}
	if updated.HWAccelOverride == nil || *updated.HWAccelOverride != accel {
		t.Fatalf("hw_accel_override = %v, want %q", updated.HWAccelOverride, accel)
	}
	if updated.HWDeviceOverride == nil || *updated.HWDeviceOverride != device {
		t.Fatalf("hw_device_override = %v, want %q", updated.HWDeviceOverride, device)
	}

	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.HWAccelOverride == nil || *reloaded.HWAccelOverride != accel {
		t.Fatalf("reloaded hw_accel_override = %v, want %q", reloaded.HWAccelOverride, accel)
	}

	// An unrelated update must not disturb the overrides: omitted means
	// unchanged, exactly like the other optional fields.
	renamed := node.Name + "-renamed"
	untouched, err := repo.Update(ctx, node.ID, UpdateNodeInput{Name: &renamed})
	if err != nil {
		t.Fatalf("rename node: %v", err)
	}
	if untouched.HWAccelOverride == nil || *untouched.HWAccelOverride != accel {
		t.Fatalf("unrelated update dropped hw_accel_override: %v", untouched.HWAccelOverride)
	}

	// The empty-string sentinel restores inheritance of the cluster setting.
	cleared, err := repo.Update(ctx, node.ID, UpdateNodeInput{
		HWAccelOverride:  new(string),
		HWDeviceOverride: new(string),
	})
	if err != nil {
		t.Fatalf("clear overrides: %v", err)
	}
	if cleared.HWAccelOverride != nil || cleared.HWDeviceOverride != nil {
		t.Fatalf("overrides after clear = %v / %v, want nil (inherit)", cleared.HWAccelOverride, cleared.HWDeviceOverride)
	}
}

// A value outside the enum must be refused before it reaches the CHECK
// constraint, so the operator sees which values are legal.
func TestRepositoryUpdateRejectsUnknownHWAccelOverride(t *testing.T) {
	repo := NewRepository(newNodeTestPool(t))
	node := createTestNode(t, repo, "hw-override-invalid")

	bogus := "videotoolbox"
	_, err := repo.Update(context.Background(), node.ID, UpdateNodeInput{HWAccelOverride: &bogus})
	if !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("err = %v, want ErrInvalidNodeInput", err)
	}

	reloaded, err := repo.GetByID(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.HWAccelOverride != nil {
		t.Fatalf("rejected update still wrote %v", reloaded.HWAccelOverride)
	}
}

// A mixed-case override is accepted like the cluster-wide setting accepts one,
// and reaches the column lowercase — the only casing its CHECK allows.
func TestRepositoryUpdateLowercasesHWAccelOverride(t *testing.T) {
	repo := NewRepository(newNodeTestPool(t))
	node := createTestNode(t, repo, "hw-override-case")

	value, device := "QSV", "/dev/dri/renderD129"
	updated, err := repo.Update(context.Background(), node.ID, UpdateNodeInput{
		HWAccelOverride:  &value,
		HWDeviceOverride: &device,
	})
	if err != nil {
		t.Fatalf("set overrides: %v", err)
	}
	if updated.HWAccelOverride == nil || *updated.HWAccelOverride != "qsv" {
		t.Fatalf("hw_accel_override = %v, want %q", updated.HWAccelOverride, "qsv")
	}
	// The device is a path, not an enum: case survives.
	if updated.HWDeviceOverride == nil || *updated.HWDeviceOverride != device {
		t.Fatalf("hw_device_override = %v, want %q", updated.HWDeviceOverride, device)
	}
}

func createTestNode(t *testing.T, repo *Repository, prefix string) *Node {
	t.Helper()
	ctx := context.Background()
	unique := time.Now().UnixNano()
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("%s-%d", prefix, unique),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://%s-%d", prefix, unique),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	return node
}

// An admin UI clears a field by sending null. Standard decoding cannot tell
// that apart from an omitted field, so the type maps it onto the clear
// sentinel; getting this wrong makes "inherit again" a silent no-op.
func TestUpdateNodeInputDecodesExplicitNullAsClear(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantAccel  *string
		wantDevice *string
	}{
		{name: "omitted leaves both unchanged", body: `{"name":"gpu-1"}`},
		{
			name:      "explicit null clears the accel override",
			body:      `{"hw_accel_override":null}`,
			wantAccel: new(string),
		},
		{
			name:       "explicit null clears both",
			body:       `{"hw_accel_override":null,"hw_device_override":null}`,
			wantAccel:  new(string),
			wantDevice: new(string),
		},
		{
			name:       "values decode normally",
			body:       `{"hw_accel_override":"qsv","hw_device_override":"/dev/dri/renderD128"}`,
			wantAccel:  ptrTo("qsv"),
			wantDevice: ptrTo("/dev/dri/renderD128"),
		},
		{
			name:      "empty string is the same clear sentinel",
			body:      `{"hw_accel_override":""}`,
			wantAccel: new(string),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var input UpdateNodeInput
			if err := json.Unmarshal([]byte(test.body), &input); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !equalStringPtr(input.HWAccelOverride, test.wantAccel) {
				t.Fatalf("HWAccelOverride = %v, want %v", input.HWAccelOverride, test.wantAccel)
			}
			if !equalStringPtr(input.HWDeviceOverride, test.wantDevice) {
				t.Fatalf("HWDeviceOverride = %v, want %v", input.HWDeviceOverride, test.wantDevice)
			}
		})
	}
}

// Decoding must not lose the fields it always carried.
func TestUpdateNodeInputKeepsExistingFields(t *testing.T) {
	var input UpdateNodeInput
	body := `{"name":"gpu-1","url":"http://gpu-1","enabled":true,"group":"rack-a","max_jobs":3,"max_bandwidth_kbps":0}`
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if input.Name == nil || *input.Name != "gpu-1" || input.URL == nil || *input.URL != "http://gpu-1" {
		t.Fatalf("identity fields lost: %+v", input)
	}
	if input.Enabled == nil || !*input.Enabled || input.Group == nil || *input.Group != "rack-a" {
		t.Fatalf("enabled/group lost: %+v", input)
	}
	if input.MaxJobs == nil || *input.MaxJobs != 3 || input.MaxBandwidthKbps == nil || *input.MaxBandwidthKbps != 0 {
		t.Fatalf("caps lost: %+v", input)
	}
}

func TestUpdateNodeInputValidate(t *testing.T) {
	for _, value := range hwAccelOverrideValues {
		if err := (UpdateNodeInput{HWAccelOverride: &value}).Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", value, err)
		}
	}
	if err := (UpdateNodeInput{}).Validate(); err != nil {
		t.Fatalf("Validate(omitted) = %v, want nil", err)
	}
	if err := (UpdateNodeInput{HWAccelOverride: new(string)}).Validate(); err != nil {
		t.Fatalf("Validate(clear) = %v, want nil", err)
	}
	bogus := "cuda"
	if err := (UpdateNodeInput{HWAccelOverride: &bogus}).Validate(); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("Validate(%q) = %v, want ErrInvalidNodeInput", bogus, err)
	}
}

// The cluster-wide playback.hw_accel accepts any casing, and the override is
// documented as taking the same values, so a third-party admin client must not
// get a 400 here for a body /admin/settings would have accepted.
func TestUpdateNodeInputValidateIgnoresCase(t *testing.T) {
	for _, value := range []string{"QSV", " Vaapi ", "NONE", "Auto"} {
		if err := (UpdateNodeInput{HWAccelOverride: &value}).Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", value, err)
		}
	}
}

// Only the acceleration enum is case-folded on the way to the column, whose
// CHECK list is lowercase. A render device is a filesystem path and keeps its
// case.
func TestNormalizeHWAccelOverride(t *testing.T) {
	if got := normalizeHWAccelOverride(" QSV "); got == nil || *got != "qsv" {
		t.Fatalf("normalizeHWAccelOverride(%q) = %v, want %q", " QSV ", got, "qsv")
	}
	if got := normalizeHWAccelOverride("   "); got != nil {
		t.Fatalf("blank override = %v, want nil (inherit)", got)
	}
	if got := normalizeOverride(" /dev/dri/renderD129 "); got == nil || *got != "/dev/dri/renderD129" {
		t.Fatalf("device override = %v, want the path unchanged", got)
	}
}

// Dispatch names the node's own override, and otherwise the cluster value
// verbatim — "auto" included, so the node resolves it against live hardware at
// session start instead of inheriting a snapshot's answer.
func TestNodeEffectiveHWAccel(t *testing.T) {
	tests := []struct {
		name    string
		node    *Node
		cluster string
		want    string
	}{
		{name: "no node at all", cluster: "qsv", want: "qsv"},
		{name: "no override inherits", node: &Node{}, cluster: "qsv", want: "qsv"},
		{name: "no override inherits auto", node: &Node{}, cluster: hwAccelAuto, want: hwAccelAuto},
		{name: "override wins", node: &Node{HWAccelOverride: ptrTo("none")}, cluster: "qsv", want: "none"},
		{name: "override wins over auto", node: &Node{HWAccelOverride: ptrTo(hwAccelNVENC)}, cluster: hwAccelAuto, want: hwAccelNVENC},
		{name: "blank override is not an override", node: &Node{HWAccelOverride: ptrTo(" ")}, cluster: "qsv", want: "qsv"},
		{
			name:    "a stale capability report is not consulted",
			node:    &Node{Capabilities: json.RawMessage(`{"resolved":"none"}`)},
			cluster: "qsv",
			want:    "qsv",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.node.EffectiveHWAccel(test.cluster); got != test.want {
				t.Fatalf("EffectiveHWAccel(%q) = %q, want %q", test.cluster, got, test.want)
			}
		})
	}
}

func ptrTo(v string) *string { return &v }

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
