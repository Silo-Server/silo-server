package debugserver

import "testing"

func TestConfigRejectsUnsafeBindingsAndSampling(t *testing.T) {
	for _, addr := range []string{"localhost:6060", ":6060", "0.0.0.0:6060", "[::]:6060", "192.0.2.1:6060", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:+6060", "[::1%lo]:6060", "127.0.0.1:pprof", "127.0.0.1:6060 "} {
		t.Run(addr, func(t *testing.T) {
			_, err := LoadConfig(func(key string) string {
				if key == "SILO_DEBUG_LISTEN" {
					return addr
				}
				return ""
			})
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, addr := range []string{"", "127.0.0.1:6060", "127.2.3.4:1", "[::1]:65535"} {
		if _, err := LoadConfig(func(key string) string {
			if key == "SILO_DEBUG_LISTEN" {
				return addr
			}
			return ""
		}); err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	for _, env := range []map[string]string{
		{"SILO_DEBUG_BLOCK_RATE": "1000000"},
		{"SILO_DEBUG_LISTEN": "127.0.0.1:6060", "SILO_DEBUG_BLOCK_RATE": "1"},
		{"SILO_DEBUG_LISTEN": "127.0.0.1:6060", "SILO_DEBUG_MUTEX_FRACTION": "1"},
		{"SILO_DEBUG_LISTEN": "127.0.0.1:6060", "SILO_DEBUG_MUTEX_FRACTION": "-1"},
		{"SILO_DEBUG_LISTEN": "127.0.0.1:6060", "SILO_DEBUG_MUTEX_FRACTION": "999999999999999999999"},
	} {
		if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
}

func TestQueryValidation(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"profile", "seconds=0"}, {"profile", "seconds=-1"}, {"profile", "seconds=61"},
		{"profile", "seconds=99999999999999999999999"}, {"profile", "seconds=1.5"},
		{"trace", "seconds=NaN"}, {"trace", "seconds=Inf"}, {"trace", "seconds=5.0001"},
		{"trace", "seconds=1e-100"}, {"heap", "seconds=1&seconds=60"},
		{"heap", "seconds="}, {"heap", "seconds=60%00"}, {"heap", "gc=2"},
		{"heap", "seconds=1&gc=1"}, {"heap", "seconds=1&debug=1"},
		{"goroutine", "debug=3"}, {"heap", "debug=2"}, {"allocs", "gc=1"},
		{"index", "seconds=1"}, {"profile", "debug=1"}, {"heap", "unknown=1"},
		{"heap", "%zz=1"}, {"heap", "seconds=1;gc=1"},
	} {
		t.Run(tc.name+"/"+tc.query, func(t *testing.T) {
			if _, err := validateQuery(tc.name, tc.query); err == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
	for _, tc := range []struct{ name, query string }{
		{"index", ""}, {"profile", ""}, {"profile", "seconds=60"}, {"trace", "seconds=.1"},
		{"trace", "seconds=5"}, {"heap", "gc=1"}, {"goroutine", "debug=2"},
		{"heap", "seconds=60&debug=0"}, {"threadcreate", "seconds=1"},
	} {
		if _, err := validateQuery(tc.name, tc.query); err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
