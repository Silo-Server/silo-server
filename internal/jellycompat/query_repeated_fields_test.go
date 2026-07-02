package jellycompat

import (
	"net/http/httptest"
	"testing"
)

// TestRepeatedFieldsParamTriggersDetail covers clients (e.g. the
// jellyfin-sdk-kotlin / Wholphin) that send Fields as repeated query params
// (Fields=A&Fields=B&...) rather than comma-separated in a single param.
// q.Get("Fields") returns only the first repeated value, which silently
// dropped every field after the first — so a request whose first Fields entry
// is a list-servable field but which also asks for a detail-only field (e.g.
// MediaSources) later never triggered the detail path and came back without
// MediaSources. parseItemsQuery must join all repeated values before splitting.
func TestRepeatedFieldsParamTriggersDetail(t *testing.T) {
	url := "/Shows/abc/Episodes?Fields=PrimaryImageAspectRatio&Fields=SeasonUserData" +
		"&Fields=ChildCount&Fields=Overview&Fields=Trickplay&Fields=SortName" +
		"&Fields=Chapters&Fields=MediaSources&Fields=MediaSourceCount" +
		"&Fields=ParentId&Fields=CanDelete&startItemId=x&limit=100"
	req := httptest.NewRequest("GET", url, nil)
	query := parseItemsQuery(req, NewResourceIDCodec())

	if !query.needsDetailFields {
		t.Fatal("repeated Fields params with a detail-only field not-first must set needsDetailFields=true")
	}
	if !query.requestedFields["mediasources"] {
		t.Error("mediasources must be parsed from repeated Fields params")
	}
	if !query.requestedFields["chapters"] {
		t.Error("chapters must be parsed from repeated Fields params")
	}
	if !query.fieldsExplicit {
		t.Error("fieldsExplicit must be true when repeated Fields params are present")
	}
	if query.startItemID != "x" {
		t.Errorf("startItemID = %q, want x", query.startItemID)
	}
}

// TestCommaSeparatedFieldsStillParsed guards the original single-param,
// comma-separated form (e.g. VidHub: Fields=A,B,C) which must keep working.
func TestCommaSeparatedFieldsStillParsed(t *testing.T) {
	req := httptest.NewRequest("GET", "/Items?Fields=Overview,MediaSources,People", nil)
	query := parseItemsQuery(req, NewResourceIDCodec())

	if !query.needsDetailFields {
		t.Fatal("comma-separated Fields with MediaSources must set needsDetailFields=true")
	}
	for _, f := range []string{"overview", "mediasources", "people"} {
		if !query.requestedFields[f] {
			t.Errorf("field %q must be parsed from comma-separated Fields", f)
		}
	}
}
