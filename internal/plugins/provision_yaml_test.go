package plugins

import (
	"errors"
	"testing"
)

func TestProvisionYAMLBounds(t *testing.T) {
	const data = "names: [&label Silo, *label]"
	encoded, err := provisionYAMLToJSON([]byte(data), 32)
	if err != nil || string(encoded) != `{"names":["Silo","Silo"]}` {
		t.Fatalf("small valid alias: %s, %v", encoded, err)
	}

	// The expanded nodes exceed the first budget. They fit the second,
	// but the quotes and punctuation of their JSON representation do not.
	for _, limit := range []int{12, 20} {
		if _, err := provisionYAMLToJSON([]byte(data), limit); !errors.Is(err, errProvisionExpandedSize) {
			t.Errorf("limit %d: want size error, got %v", limit, err)
		}
	}
}

func TestProvisionYAMLMappingKeys(t *testing.T) {
	const prefix = "name: &field label\nconfig:\n  *field: first\n"
	if _, err := provisionYAMLToJSON([]byte(prefix), 128); err != nil {
		t.Fatalf("unique aliased key: %v", err)
	}
	if _, err := provisionYAMLToJSON([]byte(prefix+"  label: second\n"), 128); err == nil {
		t.Fatal("accepted two keys with the same resolved name")
	}
}
