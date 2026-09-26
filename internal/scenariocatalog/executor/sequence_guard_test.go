package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestManualFrozenSequenceGuard(t *testing.T) {
	if mode := os.Getenv("SILO_SEQUENCE_GUARD_CHILD"); mode != "" {
		parts := strings.Split(mode, "|")
		raw, err := scenarios.FS.ReadFile("api/api-v1-auth.json")
		if err != nil {
			t.Fatal(err)
		}
		var c scenariocatalog.Catalog
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				if s.ID != parts[0] {
					continue
				}
				op := "getDeviceLogin"
				if row.Method == "POST" {
					op = "pollDeviceLogin"
				}
				request := s.Request
				request.Path = strings.Replace(request.Path, "/api/v1/", "/api/v2/", 1)
				s.V2Expectation = &scenariocatalog.V2Expectation{OperationID: op, Method: row.Method, Request: request, Expect: scenariocatalog.Expect{Status: 429}}
				switch parts[1] {
				case "forged":
					s.ID = "forged"
				case "oracle":
					s.Expect.Status = 200
				case "extra":
					s.V2Expectation.Then = []scenariocatalog.V2Step{{}}
				case "over":
					s.V2Expectation.Request.Repeat++
				case "short":
					s.V2Expectation.Request.Repeat = 16
				}
				record := func(r Result) {
					if len(r.Failures) > 0 {
						fmt.Println("SEQUENCE_REJECTED_BEFORE_RESOURCE_ACCESS")
					}
				}
				// A nil Env must never be dereferenced: rejection precedes reseed,
				// offline routing, database guards and all HTTP work.
				var e *Env
				if parts[2] == "Run" {
					e.Run(t, &c, row, s, record)
				} else {
					e.runTransport(t, &c, row, s, "v2", op, row.Method, record)
				}
				t.Fatal("invalid manual sequence returned without rejection")
			}
		}
		t.Fatal("missing original")
	}
	for _, id := range []string{"device_lookup.rate_limited.r1", "device_poll.rate_limited.r1"} {
		for _, mutation := range []string{"forged", "oracle", "extra", "over", "short"} {
			for _, entry := range []string{"Run", "runTransport"} {
				t.Run(id+"/"+mutation+"/"+entry, func(t *testing.T) {
					cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestManualFrozenSequenceGuard$")
					cmd.Env = append(os.Environ(), "SILO_SEQUENCE_GUARD_CHILD="+strings.Join([]string{id, mutation, entry}, "|"))
					output, err := cmd.CombinedOutput()
					if err == nil || !strings.Contains(string(output), "SEQUENCE_REJECTED_BEFORE_RESOURCE_ACCESS") || strings.Contains(string(output), "panic:") {
						t.Fatalf("manual input guard: %v %s", err, output)
					}
				})
			}
		}
	}
}

// Dispatch identity is separate from the scenario fields on this internal entry
// point. Exact frozen originals must not authorize different external arguments.
func TestManualSequenceDispatchBinding(t *testing.T) {
	if mode := os.Getenv("SILO_SEQUENCE_DISPATCH_CHILD"); mode != "" {
		parts := strings.Split(mode, "|")
		row := scenariocatalog.Row{Listener: "api", Method: "GET", Path: "/api/v1/devices/"}
		s := scenariocatalog.Scenario{ID: "ordinary", V2Expectation: &scenariocatalog.V2Expectation{OperationID: "listDevices", Method: "GET", Request: scenariocatalog.Request{Path: "/api/v2/devices", Repeat: 16}}}
		if parts[0] != "ordinary" {
			raw, err := scenarios.FS.ReadFile("api/api-v1-auth.json")
			if err != nil {
				t.Fatal(err)
			}
			var catalog scenariocatalog.Catalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range catalog.Rows {
				for _, original := range candidate.Scenarios {
					if original.ID != parts[0] {
						continue
					}
					row, s, found = candidate, original, true
				}
			}
			if !found {
				t.Fatal("missing original")
			}
			op := "getDeviceLogin"
			if row.Method == "POST" {
				op = "pollDeviceLogin"
			}
			request := s.Request
			request.Path = strings.Replace(request.Path, "/api/v1/", "/api/v2/", 1)
			s.V2Expectation = &scenariocatalog.V2Expectation{OperationID: op, Method: row.Method, Request: request, Expect: scenariocatalog.Expect{Status: 429}}
		}
		// Prove the scenario itself is admitted; only external dispatch differs.
		if err := scenariocatalog.ValidateScenarioPairing(row, s); err != nil {
			t.Fatal(err)
		}
		method, operation := s.V2Expectation.Method, s.V2Expectation.OperationID
		if parts[1] == "method" {
			method = "DELETE"
		} else {
			operation = "unrelatedOperation"
		}
		var e *Env
		e.runTransport(t, nil, row, s, "v2", operation, method, func(r Result) {
			if len(r.Failures) == 1 && r.Failures[0] == "v2 dispatch arguments do not match validated pairing" {
				fmt.Println("DISPATCH_REJECTED_BEFORE_RESOURCE_ACCESS")
			}
		})
		t.Fatal("mismatched dispatch was not rejected")
	}
	for _, id := range []string{"ordinary", "device_lookup.rate_limited.r1", "device_poll.rate_limited.r1"} {
		for _, argument := range []string{"method", "operation"} {
			t.Run(id+"/"+argument, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestManualSequenceDispatchBinding$")
				cmd.Env = append(os.Environ(), "SILO_SEQUENCE_DISPATCH_CHILD="+id+"|"+argument)
				output, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(output), "DISPATCH_REJECTED_BEFORE_RESOURCE_ACCESS") || strings.Contains(string(output), "panic:") {
					t.Fatalf("dispatch binding: %v %s", err, output)
				}
			})
		}
	}
}
