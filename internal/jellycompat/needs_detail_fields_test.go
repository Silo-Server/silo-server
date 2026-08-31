package jellycompat

import "testing"

func TestRequestedFieldsNeedDetail_AllowsBrowseDerivableFields(t *testing.T) {
	browseDerivable := []string{
		"mediasourcecount",
		"primaryimageaspectratio",
		"genres",
		"studios",
		"taglines",
		"providerids",
		"basicsyncinfo",
		"candelete",
		"canresume",
		"container",
		"criticrating",
		"displaypreferencesid",
		"enddate",
		"etag",
		"itemcounts",
		"originaltitle",
		"overview",
		"parentid",
		"path",
		"premieredate",
		"prefix",
		fieldProductionLocations,
		"productionyear",
		fieldSortName,
		"status",
		"tags",
	}
	for _, f := range browseDerivable {
		fields := map[string]bool{f: true}
		if requestedFieldsNeedDetail(fields) {
			t.Errorf("field %q must not trigger needsDetailFields", f)
		}
	}
}

func TestRequestedFieldsNeedDetail_RequiresDetailForRichFields(t *testing.T) {
	requireDetail := []string{
		"people",
		"chapters",
		"mediastreams", // requires file detail join we don't yet do
		"mediasources", // pending LATERAL JOIN against media_files (plan §3.2 part b)
	}
	for _, f := range requireDetail {
		fields := map[string]bool{f: true}
		if !requestedFieldsNeedDetail(fields) {
			t.Errorf("field %q should trigger needsDetailFields", f)
		}
	}
}

// Empty / nil field maps must never trigger detail.
func TestRequestedFieldsNeedDetail_EmptyDoesNotTriggerDetail(t *testing.T) {
	if requestedFieldsNeedDetail(nil) {
		t.Errorf("nil fields map must not trigger needsDetailFields")
	}
	if requestedFieldsNeedDetail(map[string]bool{}) {
		t.Errorf("empty fields map must not trigger needsDetailFields")
	}
}

// Unknown fields (not previously enumerated) must not trigger detail; the
// allowlist must be explicit on what requires detail, not the inverse.
func TestRequestedFieldsNeedDetail_UnknownFieldDoesNotTriggerDetail(t *testing.T) {
	fields := map[string]bool{"someunknownjellyfinfield": true}
	if requestedFieldsNeedDetail(fields) {
		t.Errorf("unknown field must not trigger needsDetailFields; only fields in fieldsRequiringDetail should")
	}
}

// Mixed input: a single detail-required field in a larger map must still
// trigger detail mode for the whole request.
func TestRequestedFieldsNeedDetail_MixedTriggersOnDetailField(t *testing.T) {
	fields := map[string]bool{
		"genres":   true,
		"studios":  true,
		"chapters": true, // forces detail
	}
	if !requestedFieldsNeedDetail(fields) {
		t.Errorf("mixed fields containing chapters must trigger needsDetailFields")
	}
}

// TestUnsatisfiedListFields_FlagsBrowseDroppedFields covers the diagnostic
// helper that surfaces silent client/server feature-gap drift. Fields that
// itemFromList does not populate AND that don't trigger a detail fetch are
// "unsatisfied" — the response will simply omit them.
func TestUnsatisfiedListFields_FlagsBrowseDroppedFields(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]bool
		want   []string
	}{
		{
			name:   "browse-served only",
			fields: map[string]bool{"genres": true, "studios": true, "overview": true},
			want:   nil,
		},
		{
			name:   "detail-required short-circuits (no warning)",
			fields: map[string]bool{"chapters": true, "remotetrailers": true, "externalurls": true},
			want:   nil, // detail path serves all fields
		},
		{
			name:   "list path with unserved fields",
			fields: map[string]bool{"genres": true, "remotetrailers": true, "externalurls": true, "trickplay": true},
			want:   []string{"externalurls", "remotetrailers", "trickplay"},
		},
		{
			name:   "wildcard skipped",
			fields: map[string]bool{"*": true, "remotetrailers": true},
			want:   []string{"remotetrailers"},
		},
		{
			name:   "empty input",
			fields: nil,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unsatisfiedListFields(tc.fields)
			if len(got) != len(tc.want) {
				t.Fatalf("unsatisfiedListFields(%v) = %v, want %v", tc.fields, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("unsatisfiedListFields(%v)[%d] = %q, want %q", tc.fields, i, got[i], tc.want[i])
				}
			}
		})
	}
}
