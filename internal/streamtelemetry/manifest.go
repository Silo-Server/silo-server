package streamtelemetry

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

type WalkedRoute struct {
	Method  string
	Pattern string
}

func WalkRoutes(router chi.Routes) ([]WalkedRoute, error) {
	var routes []WalkedRoute
	err := chi.Walk(router, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, WalkedRoute{Method: method, Pattern: pattern})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern == routes[j].Pattern {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Pattern < routes[j].Pattern
	})
	return routes, nil
}

func FormatManifest(routes []WalkedRoute, media []MediaRoute) string {
	declared := make(map[string]MediaRoute, len(media))
	for _, route := range media {
		declared[route.Method+" "+route.Pattern] = route
	}
	var b strings.Builder
	for _, route := range routes {
		key := route.Method + " " + route.Pattern
		if mediaRoute, ok := declared[key]; ok {
			fmt.Fprintf(&b, "%s\tmedia\t%s\t%s\t%t\t%t\n", key, mediaRoute.Class, mediaRoute.Role, mediaRoute.CapRelevant, mediaRoute.Enrolled)
		} else {
			fmt.Fprintf(&b, "%s\tnon-media\n", key)
		}
	}
	return b.String()
}

// BuildRouteManifest walks minimal and maximal fixture routers, renders the
// complete declared-or-non-media classification, and verifies that their union
// covers every declared media route.
// Delegated snapshots, when supplied, correspond one-for-one to the fixture
// routers. Each snapshot is accepted only beneath its walked wildcard mount,
// with matching HTTP method coverage. No declaration creates an observed route.
func BuildRouteManifest(routers []chi.Routes, media []MediaRoute, delegated ...map[string][]WalkedRoute) (string, error) {
	if len(routers) != 2 {
		return "", errors.New("route manifest requires minimal and maximal fixtures")
	}
	if len(delegated) != 0 && len(delegated) != len(routers) {
		return "", errors.New("delegated route snapshots must match fixtures")
	}
	seen := make(map[string]struct{})
	var b strings.Builder
	for index, router := range routers {
		routes, err := WalkRoutes(router)
		if err != nil {
			return "", err
		}
		if len(delegated) != 0 {
			parent := slices.Clone(routes)
			for mount, children := range delegated[index] {
				if !strings.HasSuffix(mount, "/*") {
					return "", fmt.Errorf("invalid delegation mount: %s", mount)
				}
				if !slices.ContainsFunc(parent, func(route WalkedRoute) bool { return route.Pattern == mount }) {
					return "", fmt.Errorf("delegation mount was not walked: %s", mount)
				}
				for _, child := range children {
					if !strings.HasPrefix(child.Pattern, strings.TrimSuffix(mount, "*")) {
						return "", fmt.Errorf("delegated route escapes mount: %s %s", child.Method, child.Pattern)
					}
					if !slices.ContainsFunc(parent, func(route WalkedRoute) bool {
						return route.Pattern == mount && (route.Method == "*" || route.Method == child.Method)
					}) {
						return "", fmt.Errorf("delegated method was not mounted: %s %s", child.Method, mount)
					}
					routes = append(routes, child)
				}
			}
			sort.Slice(routes, func(i, j int) bool {
				if routes[i].Pattern == routes[j].Pattern {
					return routes[i].Method < routes[j].Method
				}
				return routes[i].Pattern < routes[j].Pattern
			})
		}
		fmt.Fprintf(&b, "# fixture %d\n", index+1)
		b.WriteString(FormatManifest(routes, media))
		for _, route := range routes {
			seen[route.Method+" "+route.Pattern] = struct{}{}
		}
	}
	for _, route := range media {
		if _, ok := seen[route.Method+" "+route.Pattern]; !ok {
			return "", fmt.Errorf("declared media route was not walked: %s %s", route.Method, route.Pattern)
		}
	}
	return b.String(), nil
}
