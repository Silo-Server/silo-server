package mail

import (
	"regexp"
	"strings"
	"testing"
)

func TestRenderLayoutEscapesAndPlacesContent(t *testing.T) {
	out := RenderLayout(LayoutOptions{
		Preheader:  `sneak <script>alert(1)</script>`,
		Title:      `Title & <b>bold</b>`,
		BodyHTML:   `<p id="body-marker">trusted</p>`,
		FooterHTML: `<span id="footer-marker">fine print</span>`,
	})
	if strings.Contains(out, "<script>") || strings.Contains(out, "<b>bold</b>") {
		t.Fatalf("preheader/title not escaped:\n%s", out)
	}
	if !strings.Contains(out, "Title &amp; &lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("escaped title missing:\n%s", out)
	}
	if !strings.Contains(out, `<p id="body-marker">trusted</p>`) {
		t.Fatalf("body HTML not passed through:\n%s", out)
	}
	if !strings.Contains(out, `<span id="footer-marker">fine print</span>`) {
		t.Fatalf("footer HTML not passed through:\n%s", out)
	}
	if !strings.Contains(out, `src="cid:silo-logo"`) || !strings.Contains(out, `alt="Silo"`) {
		t.Fatalf("default logo missing:\n%s", out)
	}
}

// A mid-tone accent leaves both the white and the dark label under 4.5:1; the
// button must keep the accent as stored and still draw a label that reads.
func TestEmailButtonLabelReadsOnMidToneAccent(t *testing.T) {
	for _, accent := range []string{"#8b5cf6", "#777777", "#7d7d7d"} {
		out := EmailButton(Brand{AccentColor: accent}, "Sign in", "https://example.com/")
		background := regexp.MustCompile(`bgcolor="(#[0-9a-f]{6})"`).FindStringSubmatch(out)
		label := regexp.MustCompile(`;color:(#[0-9a-f]{6});`).FindStringSubmatch(out)
		if background == nil || label == nil {
			t.Fatalf("%s: button colors missing: %s", accent, out)
		}
		if background[1] != accent {
			t.Errorf("%s: button drawn on %s, want the accent as stored", accent, background[1])
		}
		lighter, darker := relativeLuminance(background[1]), relativeLuminance(label[1])
		if lighter < darker {
			lighter, darker = darker, lighter
		}
		if ratio := (lighter + 0.05) / (darker + 0.05); ratio < 4.5 {
			t.Errorf("%s: label %s on %s reads at %.2f:1, want at least 4.5:1", accent, label[1], background[1], ratio)
		}
	}
}

func TestEmailButtonEscapes(t *testing.T) {
	out := EmailButton(Brand{}, `Click "here" <now>`, `https://example.com/?a=1&b=<2>`)
	if !strings.Contains(out, `href="https://example.com/?a=1&amp;b=&lt;2&gt;"`) {
		t.Fatalf("href not escaped: %s", out)
	}
	if strings.Contains(out, "<now>") {
		t.Fatalf("label not escaped: %s", out)
	}
}
