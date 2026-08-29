package streamtelemetry

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sort"
	"sync"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/go-chi/chi/v5"
)

type Family string
type Class string
type Role string

const (
	FamilyNative        Family = "native"
	FamilyJellycompat   Family = "jellycompat"
	FamilyProxy         Family = "proxy"
	FamilyABS           Family = "abs"
	FamilyTranscodeNode Family = "transcode_node"

	ClassPlayback Class = "playback"
	ClassManifest Class = "manifest"
	ClassTransfer Class = "transfer"

	RoleViewerEgress  Role = "viewer_egress"
	RoleInternalRelay Role = "internal_relay"
	RoleProducer      Role = "producer"
)

// AllFamilies lists every declared route family, in stable sorted order. It is
// the canonical set Config.ObservesFamily and Config.ObservedFamilies fall
// back to when SILO_STREAM_TELEMETRY_FAMILIES is unset, so the five families
// are named once rather than duplicated across both functions.
var AllFamilies = []Family{FamilyABS, FamilyJellycompat, FamilyNative, FamilyProxy, FamilyTranscodeNode}

type MediaRoute struct {
	Family              Family
	Method              string
	Pattern             string
	Class               Class
	Role                Role
	CanonicalSessionKey string
	CapRelevant         bool
	Enrolled            bool
	Capture             func(*http.Request) CaptureSet
}

type routeKey struct {
	family  Family
	method  string
	pattern string
}

var declarations = struct {
	sync.RWMutex
	routes map[routeKey]MediaRoute
}{routes: make(map[routeKey]MediaRoute)}

func DeclareRoutes(routes ...MediaRoute) {
	declarations.Lock()
	defer declarations.Unlock()
	for _, route := range routes {
		key := routeKey{route.Family, route.Method, route.Pattern}
		if existing, ok := declarations.routes[key]; ok {
			if !sameDeclaration(existing, route) {
				panic(fmt.Sprintf("conflicting media route declaration: %s %s %s", route.Family, route.Method, route.Pattern))
			}
			continue
		}
		declarations.routes[key] = route
	}
}

func DeclaredRoutes(family Family) []MediaRoute {
	declarations.RLock()
	defer declarations.RUnlock()
	routes := make([]MediaRoute, 0)
	for _, route := range declarations.routes {
		if route.Family == family {
			routes = append(routes, route)
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern == routes[j].Pattern {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Pattern < routes[j].Pattern
	})
	return routes
}

func sameDeclaration(a, b MediaRoute) bool {
	return a.Family == b.Family && a.Method == b.Method && a.Pattern == b.Pattern &&
		a.Class == b.Class && a.Role == b.Role &&
		a.CanonicalSessionKey == b.CanonicalSessionKey &&
		a.CapRelevant == b.CapRelevant && a.Enrolled == b.Enrolled &&
		capturePointer(a.Capture) == capturePointer(b.Capture)
}

func capturePointer(capture func(*http.Request) CaptureSet) uintptr {
	if capture == nil {
		return 0
	}
	return reflect.ValueOf(capture).Pointer()
}

func genericCapture(r *http.Request) CaptureSet {
	pattern := ""
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		pattern = routeContext.RoutePattern()
	}
	return CaptureSet{
		Method:     r.Method,
		Pattern:    pattern,
		ViewerIP:   viewerIP(r),
		UserAgent:  r.UserAgent(),
		ReceivedAt: now(),
	}
}

// ViewerIP resolves the address to record for the person on the other end of a
// media route: the resolved client IP when clientip.Middleware has run, and the
// transport peer otherwise. Exported because every family's Capture builds the
// same fallback chain, and four copies of it would diverge the moment one is
// fixed (IPv6 bracket handling, say) and the others are not.
func ViewerIP(r *http.Request) string { return viewerIP(r) }

func viewerIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip := clientip.FromContext(r.Context()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
