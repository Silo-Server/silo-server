package provisiondoc

import (
	"errors"
	"testing"
)

func TestYAMLBounds(t *testing.T) {
	const data = "names: [&label Silo, *label]"
	encoded, err := YAMLToJSON([]byte(data), 32, nil)
	if err != nil || string(encoded) != `{"names":["Silo","Silo"]}` {
		t.Fatalf("small valid alias: %s, %v", encoded, err)
	}

	// The expanded nodes exceed the first budget. They fit the second,
	// but the quotes and punctuation of their JSON representation do not.
	for _, limit := range []int{12, 20} {
		if _, err := YAMLToJSON([]byte(data), limit, nil); !errors.Is(err, ErrExpandedSize) {
			t.Errorf("limit %d: want size error, got %v", limit, err)
		}
	}
}

func TestYAMLMappingKeys(t *testing.T) {
	const prefix = "name: &field label\nconfig:\n  *field: first\n"
	if _, err := YAMLToJSON([]byte(prefix), 128, nil); err != nil {
		t.Fatalf("unique aliased key: %v", err)
	}
	if _, err := YAMLToJSON([]byte(prefix+"  label: second\n"), 128, nil); err == nil {
		t.Fatal("accepted two keys with the same resolved name")
	}
}
