package workersource

import "testing"

func TestSourceRevisionBindsOwnerIncarnation(t *testing.T) {
	owner, path, _ := testAuthority(t, "0123456789", Limits{})
	options := owner.options
	options.OwnerID = ""
	other, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	first, err := owner.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	same, err := owner.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref != same.Ref {
		t.Fatal("same-owner grant renewal changed immutable source revision")
	}
	if first.Ref.ID != foreign.Ref.ID || first.Ref.Revision == foreign.Ref.Revision {
		t.Fatal("another owner reproduced a host-local source revision")
	}
	left, err := owner.options.Store.Get(t.Context(), first.Grant)
	if err != nil {
		t.Fatal(err)
	}
	right, err := other.options.Store.Get(t.Context(), foreign.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if left.Identity != right.Identity || left.OwnerID == right.OwnerID {
		t.Fatal("fixture did not exercise identical local metadata across different owners")
	}
}
