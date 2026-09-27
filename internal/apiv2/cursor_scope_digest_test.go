package apiv2

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// A cursor minted under one set of maturity limits must not resume under
// another: each limit changes which rows the page walks over. Profiles bump
// PolicyRevision when a limit changes, but a policy override can change the
// effective limit without one, so the digest carries the limits themselves.
func TestViewerScopeDigestCoversMaturityLimits(t *testing.T) {
	digest := func(limits access.MaturityLimits) string {
		return viewerScopeDigest(access.SetScope(context.Background(), access.Scope{MaturityLimits: limits}))
	}
	base := digest(access.MaturityLimits{})
	for name, limits := range map[string]access.MaturityLimits{
		"content rating": {MaxContentRating: "PG"},
		"unrated":        {AllowUnratedContent: true},
		"advisory age":   {MaxAdvisoryAge: 10},
	} {
		if digest(limits) == base {
			t.Errorf("setting the %s limit does not change the cursor scope digest", name)
		}
	}
	if digest(access.MaturityLimits{MaxAdvisoryAge: 10}) == digest(access.MaturityLimits{MaxAdvisoryAge: 13}) {
		t.Error("advisory limits 10 and 13 must digest differently")
	}
	// Requiring an advisory age walks a different, smaller set of rows.
	if digest(access.MaturityLimits{MaxAdvisoryAge: 10}) == digest(access.MaturityLimits{MaxAdvisoryAge: 10, RequireAdvisoryAge: true}) {
		t.Error("requiring an advisory age must change the cursor scope digest")
	}
	// Without a limit the flag renders no SQL, so it must not strand cursors.
	if digest(access.MaturityLimits{RequireAdvisoryAge: true}) != base {
		t.Error("a require flag with no limit changed the cursor scope digest")
	}
}
