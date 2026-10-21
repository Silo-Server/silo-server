package imagesize

import "testing"

// A target width resolves to the narrowest rung of the image's own ladder at
// least that wide, and to the original past the widest rung.
func TestVariantForTargetWidth(t *testing.T) {
	tests := []struct {
		imageType string
		px        int
		want      string
	}{
		{"backdrop", 1344, "w1920"},
		{"backdrop", 1000, "w1280"},
		{"backdrop", 2560, "w2560"},
		{"backdrop", 2688, "original"},
		{"still", 1344, "original"},
		{"still", 720, "w1280"},
		{"still", 360, "w500"},
		{"poster", 352, "w500"},
		{"poster", 176, "w300"},
		{"logo", 880, "w1280"},
		{"logo", 1760, "original"},
		{"profile", 400, "w500"},
		{"profile", 200, "w300"},
	}
	for _, tt := range tests {
		if got := Variant(tt.imageType, Width(tt.px)); got != tt.want {
			t.Errorf("Variant(%q, Width(%d)) = %q, want %q", tt.imageType, tt.px, got, tt.want)
		}
	}
}

func TestRequestForSlot(t *testing.T) {
	req := Request{Size: Medium, Widths: Widths{Backdrop: 2688, Logo: 1760}}
	if got := req.For(SlotBackdrop); got != Width(2688) {
		t.Errorf("backdrop = %q, want Width(2688)", got)
	}
	if got := req.For(SlotLogo); got != Width(1760) {
		t.Errorf("logo = %q, want Width(1760)", got)
	}
	// A slot without a width keeps the response-wide size, Unset included.
	if got := req.For(SlotPoster); got != Medium {
		t.Errorf("poster = %q, want medium", got)
	}
	if got := (Request{}).For(SlotStill); got != Unset {
		t.Errorf("empty request still = %q, want Unset", got)
	}
}

// Clients choose widths through the slot parameters only; image_size never
// accepts a width.
func TestParseRejectsWidthSizes(t *testing.T) {
	if _, err := Parse(string(Width(500))); err == nil {
		t.Fatal("Parse accepted a width size")
	}
}

func TestWidthsEncodeIsCanonical(t *testing.T) {
	if got := (Widths{}).Encode(); got != "" {
		t.Errorf("zero widths encode as %q, want empty", got)
	}
	if got, want := (Widths{Photo: 200, Backdrop: 1344}).Encode(), "backdrop=1344,photo=200"; got != want {
		t.Errorf("Encode = %q, want %q", got, want)
	}
}

func TestPluginVariantForWidth(t *testing.T) {
	for px, want := range map[int]string{
		200:  PluginVariantCard,
		500:  PluginVariantFeatured,
		1280: PluginVariantLarge,
		2688: PluginVariantOriginal,
	} {
		if got := PluginVariant(Width(px)); got != want {
			t.Errorf("PluginVariant(Width(%d)) = %q, want %q", px, got, want)
		}
	}
}

func TestPhotoPluginVariant(t *testing.T) {
	for _, tc := range []struct {
		size Size
		want string
	}{
		{Unset, PluginVariantLarge},
		{Width(200), PluginVariantLarge},
		{Width(1280), PluginVariantLarge},
		{Width(1281), PluginVariantOriginal},
		{Small, PluginVariantCard},
		{Medium, PluginVariantFeatured},
		{Original, PluginVariantOriginal},
	} {
		if got := PhotoPluginVariant(tc.size); got != tc.want {
			t.Errorf("PhotoPluginVariant(%q) = %q, want %q", tc.size, got, tc.want)
		}
	}
}
