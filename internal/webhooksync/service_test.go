package webhooksync

import (
	"testing"
)

func TestResolveWebhookProfileRequiresExplicitMapping(t *testing.T) {
	t.Parallel()

	linkedProfileID := "linked-profile"

	cases := []struct {
		name    string
		mapping *ProfileMapping
		want    string
		wantOK  bool
	}{
		{
			name:   "missing mapping is skipped",
			wantOK: false,
		},
		{
			name:    "unmapped external user is skipped",
			mapping: &ProfileMapping{},
			wantOK:  false,
		},
		{
			name:    "empty profile mapping is skipped",
			mapping: &ProfileMapping{SiloProfileID: ptrString("")},
			wantOK:  false,
		},
		{
			name:    "explicit profile mapping is used",
			mapping: &ProfileMapping{SiloProfileID: &linkedProfileID},
			want:    linkedProfileID,
			wantOK:  true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := resolveWebhookProfileID(tc.mapping)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("resolveWebhookProfileID() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func ptrString(value string) *string {
	return &value
}
