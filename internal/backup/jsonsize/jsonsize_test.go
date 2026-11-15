package jsonsize

import (
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type profile struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Libs     []int                      `json:"allowed_library_ids"`
	Data     map[string]json.RawMessage `json:"data"`
	Settings [16]string                 `json:"-"`
}

type userDoc struct {
	Profiles []profile `json:"profiles"`
	Items    []string  `json:"items"`
}

var limits = Limits{Depth: 32, Bytes: 4 << 20, Elements: 100_000}

// TestCheckRefusesDocumentsThatDecodeLarge: a short document of empty
// elements is refused for what it would decode to, quickly and without
// allocating it; a document of the same shape within the bound passes, and
// the estimate covers what decoding it takes.
func TestCheckRefusesDocumentsThatDecodeLarge(t *testing.T) {
	bomb := []byte(`{"profiles":[` + strings.Repeat(`{},`, 99_999) + `{}]}`)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Check(bomb, &userDoc{}, limits)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("100,000 empty profiles in %d bytes: %v, want ErrTooLarge", len(bomb), err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("checking allocated %d bytes, want under 1 MiB", allocated)
	}

	ok := []byte(`{"profiles":[{"id":"p1","Name":"A","allowed_library_ids":[1,2],"data":{"ratings":{"r":[1]}}}],"items":["a","b"]}`)
	estimate, err := Check(ok, &userDoc{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// One profile, its library ids, its data entry, and two items.
	if estimate < int64(2*profileSize()) {
		t.Fatalf("estimate %d is below one profile's size", estimate)
	}

	for name, doc := range map[string]string{
		"deep":        strings.Repeat("[", 40) + strings.Repeat("]", 40),
		"wide object": `{"profiles":[{"data":{` + strings.Repeat(`"k":1,`, 100_000) + `"k":1}}]}`,
		"long array":  `{"items":[` + strings.Repeat(`"",`, 100_000) + `""]}`,
	} {
		if _, err := Check([]byte(doc), &userDoc{}, limits); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%s: %v, want ErrTooLarge", name, err)
		}
	}
	for _, doc := range []string{`{"items":[`, `{"items" 1}`, `[1,]`, `"open`} {
		if _, err := Check([]byte(doc), &userDoc{}, limits); err == nil {
			t.Fatalf("%q passed", doc)
		}
	}
}

func profileSize() uintptr { return reflect.TypeFor[profile]().Size() }
