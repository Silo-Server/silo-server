package handlers

import (
	"bytes"
	"encoding/json"
)

type optionalNullableString struct {
	set   bool
	value *string
}

// SetValue lets typed adapters preserve omitted, null, and value states
// without round-tripping an entire request through JSON.
func (o *optionalNullableString) SetValue(value *string, set bool) {
	o.set = set
	if value == nil {
		o.value = nil
		return
	}
	v := *value
	o.value = &v
}

func (o *optionalNullableString) UnmarshalJSON(data []byte) error {
	o.set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.value = &value
	return nil
}

func (o optionalNullableString) Set() bool {
	return o.set
}

func (o optionalNullableString) Value() *string {
	return o.value
}
