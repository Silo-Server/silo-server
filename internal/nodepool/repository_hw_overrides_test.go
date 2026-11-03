package nodepool

import (
	"encoding/json"
	"errors"
	"testing"
)

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
			wantAccel:  strPtr("qsv"),
			wantDevice: strPtr("/dev/dri/renderD128"),
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
		{name: "override wins", node: &Node{HWAccelOverride: strPtr("none")}, cluster: "qsv", want: "none"},
		{name: "override wins over auto", node: &Node{HWAccelOverride: strPtr(hwAccelNVENC)}, cluster: hwAccelAuto, want: hwAccelNVENC},
		{name: "blank override is not an override", node: &Node{HWAccelOverride: strPtr(" ")}, cluster: "qsv", want: "qsv"},
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

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
