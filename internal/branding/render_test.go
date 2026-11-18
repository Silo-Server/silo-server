package branding

import (
	"strings"
	"testing"
)

func newSnapshot(name string) Snapshot {
	return Snapshot{ServerName: name, assets: map[AssetKind]string{}}
}

func TestRenderIndexHTMLEscapesTitle(t *testing.T) {
	in := []byte(`<title>Silo</title></head>`)
	out := string(RenderIndexHTML(in, newSnapshot(`A&B<script>`)))
	if strings.Contains(out, "<script>") {
		t.Fatalf("title not escaped: %q", out)
	}
	if !strings.Contains(out, "A&amp;B&lt;script&gt;") {
		t.Fatalf("expected escaped title, got: %q", out)
	}
}
