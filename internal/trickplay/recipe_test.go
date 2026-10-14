package trickplay

import (
	"math"
	"regexp"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestNewRecipeClampsSettings(t *testing.T) {
	tests := []struct {
		width, interval int
		want            Recipe
	}{
		{300, 10, Recipe{Width: 300, IntervalMS: 10000}},
		{301, 10, Recipe{Width: 300, IntervalMS: 10000}},
		{50, 1, Recipe{Width: 160, IntervalMS: 5000}},
		{4000, 600, Recipe{Width: 640, IntervalMS: 60000}},
	}
	for _, tt := range tests {
		if got := NewRecipe(tt.width, tt.interval); got != tt.want {
			t.Errorf("NewRecipe(%d, %d) = %+v, want %+v", tt.width, tt.interval, got, tt.want)
		}
	}
	if got := NewRecipe(300, 10).String(); got != "v1/w300/i10000/q80" {
		t.Errorf("recipe string %q", got)
	}
}

func TestRecipeGrid(t *testing.T) {
	for width, want := range map[int][2]int{160: {10, 8}, 300: {10, 8}, 320: {10, 8}, 400: {8, 8}, 480: {6, 6}, 640: {5, 5}} {
		columns, rows := Recipe{Width: width}.Grid()
		if columns != want[0] || rows != want[1] || columns*width > maxSheetWidth {
			t.Errorf("width %d: grid %dx%d, want %dx%d within %d px", width, columns, rows, want[0], want[1], maxSheetWidth)
		}
	}
}

func TestRecipeGridFitsTallDecodedTiles(t *testing.T) {
	for _, width := range []int{MinWidth, DefaultWidth, 320, 400, 480, MaxWidth} {
		r := NewRecipe(width, DefaultIntervalSeconds)
		columns, rows := r.Grid()
		req := mediasample.Request{
			Input:   "/media/portrait.mkv",
			Samples: &mediasample.Samples{Seconds: []float64{5}},
			Sheets: &mediasample.SheetsOutput{TileWidth: r.Width, TileHeight: r.TileHeight(0.05),
				Columns: columns, Rows: rows, Quality: Quality, UseInputAspect: true},
		}
		if err := req.Validate(); err != nil {
			t.Errorf("width %d cannot hold the tallest decoded tiles: %v", width, err)
		}
	}
}

func TestRecipeTileHeight(t *testing.T) {
	r := Recipe{Width: 300}
	for aspect, want := range map[float64]int{16.0 / 9: 168, 2.39: 126, 4.0 / 3: 226, 9.0 / 16: 534, 0: 168, math.Inf(1): 168, 40: 16} {
		if got := r.TileHeight(aspect); got != want {
			t.Errorf("aspect %.3f: height %d, want %d", aspect, got, want)
		}
	}
}

func TestRecipeSampleTimes(t *testing.T) {
	r := Recipe{Width: 300, IntervalMS: 10000}
	if got := r.SampleTimes(35); !slices.Equal(got, []float64{5, 15, 25, 34}) {
		t.Errorf("35 s: %v", got)
	}
	if got := r.SampleTimes(30); !slices.Equal(got, []float64{5, 15, 25}) {
		t.Errorf("30 s: %v", got)
	}
	if got := r.SampleTimes(3); !slices.Equal(got, []float64{2}) {
		t.Errorf("3 s: %v", got)
	}
	if got := r.SampleTimes(0.5); !slices.Equal(got, []float64{0}) {
		t.Errorf("0.5 s: %v", got)
	}
	if got := r.SampleTimes(0); got != nil {
		t.Errorf("0 s: %v", got)
	}
	times := r.SampleTimes(7201)
	if len(times) != 721 {
		t.Fatalf("2 h: %d samples, want 721", len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			t.Fatalf("times not increasing at %d: %v", i, times[i-1:i+1])
		}
		if int(times[i]*1000)/r.IntervalMS != i {
			t.Fatalf("sample %d at %.3f s lies outside its interval", i, times[i])
		}
	}
}

func TestParseAspectRatio(t *testing.T) {
	for value, want := range map[string]float64{"16:9": 16.0 / 9, "2.39:1": 2.39, "12:5": 2.4, " 4:3 ": 4.0 / 3} {
		if got, ok := ParseAspectRatio(value); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%q: %v %t, want %v", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "0:1", "N/A", "16/9", "a:b", "16:0"} {
		if _, ok := ParseAspectRatio(value); ok {
			t.Errorf("%q parsed", value)
		}
	}
}

// TestSheetKeys pins the key shape the artwork route and the deletion queue
// rely on: the revision in the file name makes the sheet immutable, and the
// revision prefix is one the queue accepts.
func TestSheetKeys(t *testing.T) {
	key := SheetKey(42, 9913, 3)
	if key != "trickplay/42/9913/3.9913.jpg" {
		t.Fatalf("key %q", key)
	}
	if rev := artworkkey.Revision(key); rev != "9913" {
		t.Fatalf("artworkkey.Revision(%q) = %q, want the revision", key, rev)
	}
	queueCheck := regexp.MustCompile(`^trickplay/[1-9][0-9]*/[1-9][0-9]*/$`)
	for range 100 {
		prefix := revisionPrefix(42, newRevision())
		if !queueCheck.MatchString(prefix) {
			t.Fatalf("prefix %q fails the queue's check", prefix)
		}
	}
	if !IsKey(key) || IsKey("chapter-images/42/0/w300.webp") {
		t.Fatal("IsKey")
	}
}
