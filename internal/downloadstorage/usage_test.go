package downloadstorage

import (
	"testing"
)

func TestRedactPathDropsDirectory(t *testing.T) {
	u := Usage{Dir: "/srv/x", Files: 3}
	if r := u.RedactPath(); r.Dir != "" || r.Files != 3 {
		t.Fatalf("RedactPath = %+v", r)
	}
}
