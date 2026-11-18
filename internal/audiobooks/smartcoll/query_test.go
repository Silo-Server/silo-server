package smartcoll

import "testing"

func TestValidate_PersonalizedWithoutScope(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}}
	if err := q.Validate(false); err == nil {
		t.Errorf("expected error for personalized without scope")
	}
}
