package nodecontrol

import (
	"reflect"
)

// nodeConfigReadsNotCarried are the config fields the transcode node reads
// that the document deliberately does not carry, each with the reason. It is
// empty: a field belongs here only with a reason the node can run without it.
var nodeConfigReadsNotCarried = map[string]string{}

// fillNonZero sets every leaf of v to a value other than its zero.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			fillNonZero(v.Field(i))
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	default:
		panic("fillNonZero: unhandled kind " + v.Kind().String())
	}
}
