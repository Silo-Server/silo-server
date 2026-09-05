package routeinventory

import (
	"errors"
	"os"
	"path/filepath"
)

// ArtifactPath is the committed inventory, relative to the repository root.
const ArtifactPath = "contracts/api/v2/route-inventory.json"

// Entry function names of the listeners and of the constructions that are
// deliberately excluded. They are spelled here, separately from the chi
// constructor names in analyze.go, so a coincidence of spelling between a Silo
// function and a chi constructor cannot make one constant mean two things.
const (
	apiRouterFunc            = "NewRouter"
	apiRouterCtor            = "newChiRouter"
	nodeHandlerRecv          = "Server"
	nodeHandlerFunc          = "Handler"
	nodeHandlerCtor          = "router"
	rootHandlerFunc          = "newRootHandler"
	rootHandlerCtor          = "newRootMux"
	absListenerFunc          = "newAudiobookshelfListener"
	jellycompatRouterFunc    = "NewRouter"
	jellycompatRouterFile    = "internal/jellycompat/router.go"
	absListenerFile          = "cmd/silo/abs_listener.go"
	cmdSiloDir               = "cmd/silo"
	internalAPIDir           = "internal/api"
	internalProxyDir         = "internal/proxy"
	internalTranscodeNodeDir = "internal/transcodenode"
)

// DefaultConfig describes the legacy native HTTP surface: the listeners Silo
// serves natively, the packages that register on them, and the router
// constructions that are deliberately out of scope.
//
// The compatibility listeners below are excluded on purpose. Jellyfin and
// Audiobookshelf are external wire contracts Silo implements, not Silo's own
// API, and the v2 program leaves them untouched. They still need an explicit
// entry: without one, the stray-router audit would fail, which is the point —
// a new listener has to be classified rather than ignored. Each exclusion names
// one function in one file, so it cannot silently cover a router someone adds
// beside it later.
func DefaultConfig(root string) Config {
	return Config{
		Root: root,
		Listeners: []ListenerSpec{
			{
				ID:   ListenerRoot,
				Kind: ListenerKindServeMux,
				Description: "Process root listener on the primary port: the http.ServeMux that serves /metrics, " +
					"delegates /api/ to the API listener, and serves the frontend at /.",
				Dir:         cmdSiloDir,
				Func:        rootHandlerFunc,
				Constructor: rootHandlerCtor,
				Delegates:   map[string]string{"apiRouter": ListenerAPI},
			},
			{
				ID:          ListenerAPI,
				Description: "Main Silo API listener: the /api/v1 namespace and the routes mounted beside it.",
				Dir:         internalAPIDir,
				Func:        apiRouterFunc,
				Constructor: apiRouterCtor,
			},
			{
				ID:          ListenerProxy,
				Description: "Proxy node listener: media relay, node control, and its own health/metrics probes.",
				Dir:         internalProxyDir,
				Recv:        nodeHandlerRecv,
				Func:        nodeHandlerFunc,
				Constructor: nodeHandlerCtor,
			},
			{
				ID:          ListenerTranscodeNode,
				Description: "Transcode node listener: transcode/remux session control, artifacts, and probes.",
				Dir:         internalTranscodeNodeDir,
				Recv:        nodeHandlerRecv,
				Func:        nodeHandlerFunc,
				Constructor: nodeHandlerCtor,
			},
		},
		AuditDirs: []string{
			internalAPIDir,
			"internal/api/handlers",
			internalProxyDir,
			internalTranscodeNodeDir,
			cmdSiloDir,
		},
		Exclusions: []RouterExclusion{
			{
				File: jellycompatRouterFile,
				Func: jellycompatRouterFunc,
				Reason: "Jellyfin-protocol compatibility listener; an external wire contract, " +
					"out of scope for the native v2 migration",
			},
			{
				File: absListenerFile,
				Func: absListenerFunc,
				Reason: "Audiobookshelf-protocol compatibility listener; an external wire contract, " +
					"out of scope for the native v2 migration",
			},
		},
	}
}

// FindRepoRoot walks up from dir until it finds the module's go.mod.
func FindRepoRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", errors.New("no go.mod found above " + dir)
		}
		abs = parent
	}
}
