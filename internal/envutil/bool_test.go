package envutil

import "testing"

const testEnv = "SILO_ENVUTIL_TEST_FLAG"

func TestTruthy(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {" True ", true},
		{"yes", true}, {"on", true}, {"enabled", true},
		{"", false}, {"   ", false}, {"0", false}, {"false", false},
		{"no", false}, {"off", false}, {"disabled", false}, {"flase", false},
	} {
		if got := Truthy(test.value); got != test.want {
			t.Errorf("Truthy(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestBool(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		if Bool(testEnv) {
			t.Fatal("unset variable read as on")
		}
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv(testEnv, "yes")
		if !Bool(testEnv) {
			t.Fatal("truthy variable read as off")
		}
	})
}

// A default-on flag is only safe if a garbled value lands on "off": the operator
// who mistyped the kill switch was reaching for off, not for the default.
func TestBoolDefault(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		set   bool
		def   bool
		want  bool
	}{
		{"unset keeps a true default", "", false, true, true},
		{"unset keeps a false default", "", false, false, false},
		{"empty keeps the default", "", true, true, true},
		{"whitespace keeps the default", "   ", true, true, true},
		{"explicit false overrides a true default", "false", true, true, false},
		{"explicit true overrides a false default", "true", true, false, true},
		{"malformed value reads as off under a true default", "flase", true, true, false},
		{"malformed value reads as off under a false default", "flase", true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.set {
				t.Setenv(testEnv, test.value)
			}
			if got := BoolDefault(testEnv, test.def); got != test.want {
				t.Fatalf("BoolDefault(%q, %v) = %v, want %v", test.value, test.def, got, test.want)
			}
		})
	}
}

func TestIsSet(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"unset", "", false, false},
		{"empty", "", true, false},
		{"whitespace only", " \t ", true, false},
		{"false is still set", "false", true, true},
		{"true is set", "true", true, true},
		{"garbage is set", "flase", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.set {
				t.Setenv(testEnv, test.value)
			}
			if got := IsSet(testEnv); got != test.want {
				t.Fatalf("IsSet(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
