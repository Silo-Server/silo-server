package apiv2

import (
	"net/http"
	"strings"
	"testing"
)

func TestDemoRestrictionRejectsSafeMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		op := Operation{Operation: humaOp(method, Prefix+"/demo-probe", "readDemoProbe", "test", ""), Class: ClassAuthenticated, DemoRestricted: true}
		if err := checkOperation(op); err == nil || !strings.Contains(err.Error(), "demo restriction is inert") {
			t.Fatalf("method %s accepted inert demo annotation: %v", method, err)
		}
	}
}
