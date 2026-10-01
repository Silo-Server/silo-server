package scenariocatalog

import (
	"fmt"
	"testing"
)

func deviceStep() V2Step {
	return V2Step{OperationID: "listDevices", Step: Step{Method: "GET", Request: Request{Path: "/api/v2/devices"}, Expect: Expect{Status: 200}}}
}
func TestV2FollowupBoundsAndPlacement(t *testing.T) {
	for _, tc := range []struct {
		name            string
		primary, follow int
		ok              bool
	}{{"sixteen", 8, 8, true}, {"seventeen", 8, 9, false}, {"primary alone", 17, 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &V2Expectation{Request: Request{Repeat: tc.primary}}
			if tc.follow > 0 {
				s := deviceStep()
				s.Request.Repeat = tc.follow
				p.Then = []V2Step{s}
			}
			if err := ValidateV2Sequence(p); (err == nil) != tc.ok {
				t.Fatalf("bounds: %v", err)
			}
		})
	}
	p := &V2Expectation{Request: Request{Repeat: 1}}
	for range 15 {
		p.Then = append(p.Then, deviceStep())
	}
	if err := ValidateV2Sequence(p); err != nil {
		t.Fatal(err)
	}
	p.Then = append(p.Then, deviceStep())
	if ValidateV2Sequence(p) == nil {
		t.Fatal("17 steps accepted")
	}
	valid := ResponseBinding{Pointer: new("/page/next_cursor"), Query: "cursor"}
	for _, tc := range []struct {
		name string
		b    ResponseBinding
		ok   bool
	}{
		{"cursor", valid, true}, {"etag", ResponseBinding{Header: "ETag", RequestHeader: capturedIfMatch}, true},
		{"auth", ResponseBinding{Header: "ETag", RequestHeader: "Authorization"}, false},
		{"profile", ResponseBinding{Header: "ETag", RequestHeader: "X-Profile-Id"}, false},
		{"host", ResponseBinding{Header: "ETag", RequestHeader: "Host"}, false},
		{"missing source", ResponseBinding{Query: "cursor"}, false},
		{"ambiguous source", ResponseBinding{Header: "ETag", Pointer: new("/x"), Query: "cursor"}, false},
		{"ambiguous target", ResponseBinding{Header: "ETag", RequestHeader: capturedIfMatch, Query: "cursor"}, false},
		{"invalid escape", ResponseBinding{Pointer: new("/a~2b"), Query: "cursor"}, false},
		{"undeclared query", ResponseBinding{Pointer: new("/x"), Query: "surprise"}, false},
		{"integer query", ResponseBinding{Pointer: new("/x"), Query: "limit"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := deviceStep()
			s.FromPrevious = []ResponseBinding{tc.b}
			if err := ValidateV2Bindings(s); (err == nil) != tc.ok {
				t.Fatalf("validation = %v", err)
			}
		})
	}
	for _, request := range []Request{{Path: "/api/v2/devices", Query: map[string]string{"cursor": "static"}}, {Path: "/api/v2/devices?cursor=static"}} {
		if ValidateResponseBindings(request, []ResponseBinding{valid}) == nil {
			t.Fatal("ambiguous static target accepted")
		}
	}
	eight := []ResponseBinding{}
	for i := range MaxResponseBindings {
		eight = append(eight, ResponseBinding{Pointer: new("/cursor"), Query: fmt.Sprintf("query%d", i)})
	}
	if err := ValidateResponseBindings(Request{}, eight); err != nil {
		t.Fatal(err)
	}
	bindings := []ResponseBinding{}
	for range 9 {
		bindings = append(bindings, valid)
	}
	if ValidateResponseBindings(Request{}, bindings) == nil {
		t.Fatal("nine captures accepted")
	}
	if ValidateResponseBindings(Request{}, []ResponseBinding{valid, valid}) == nil {
		t.Fatal("duplicate destination accepted")
	}
}
