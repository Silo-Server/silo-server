package pluginhost

import (
	"os"
	"strings"
)

// processEnvNames are the server environment variables a plugin process
// receives. Everything else, including DATABASE_URL, SECRET_KEY and
// REDIS_URL, stays with the server; a plugin takes its settings from its
// config. The list covers locating executables, temp and home directories,
// time zone and locale, CA certificates, and the outbound proxy.
var processEnvNames = map[string]bool{
	"PATH":          true,
	"HOME":          true,
	"TMPDIR":        true,
	"TZ":            true,
	"LANG":          true,
	"SSL_CERT_FILE": true,
	"SSL_CERT_DIR":  true,
	"HTTP_PROXY":    true,
	"HTTPS_PROXY":   true,
	"NO_PROXY":      true,
	"http_proxy":    true,
	"https_proxy":   true,
	"no_proxy":      true,
}

// ProcessEnv returns the environment for a plugin process: the allowlisted
// subset of the server's own. Callers starting a plugin binary set it as the
// command's Env, so the child never sees the rest.
func ProcessEnv() []string {
	// Never nil: a nil exec.Cmd.Env inherits the whole environment.
	env := []string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if processEnvNames[name] || strings.HasPrefix(name, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}
