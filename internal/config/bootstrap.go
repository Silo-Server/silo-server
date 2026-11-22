package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/envutil"
)

// minSecretKeyLen is the minimum acceptable SECRET_KEY length (in characters).
// It mirrors secret.MinMasterKeyLen; the package is not imported here to keep
// the bootstrap loader dependency-free, so the two constants must stay in sync.
const minSecretKeyLen = 32

// modeProxy is the server mode of a proxy node. A transcode node never loads
// this bootstrap: it starts from SILO_CORE_URL and SILO_NODE_KEY alone.
const modeProxy = "proxy"

// BootstrapConfig holds the minimal config needed before database connection.
type BootstrapConfig struct {
	DatabaseURL string
	RedisURL    string // optional override; empty means use DB setting
	Listen      string
	JFListen    string
	ABSListen   string
	Mode        string
	// RedisURLVar is the variable RedisURL came from: REDIS_URL_FILE when the
	// URL was read from a file, otherwise REDIS_URL, even when RedisURL is empty.
	RedisURLVar string
	// SecretKey is the master key (raw SECRET_KEY env value) from which the
	// at-rest credential cipher derives its data key. It lives outside Postgres
	// so encrypted secrets survive a full database compromise/dump.
	SecretKey []byte
}

// LoadDatabaseURL loads DATABASE_URL from a .env file (if it exists) and
// environment variables, for maintenance commands that only reach the
// database and must not need the server's other bootstrap secrets.
// DATABASE_URL_FILE can name a file holding it instead.
func LoadDatabaseURL(envFile string) (string, error) {
	if envFile != "" {
		_ = godotenv.Load(envFile)
	}
	dbURL, _, err := envutil.LookupSecret(os.Getenv, "DATABASE_URL")
	if err != nil {
		return "", err
	}
	if dbURL == "" {
		return "", fmt.Errorf("DATABASE_URL is required (set in .env or environment)")
	}
	return dbURL, nil
}

// LoadBootstrap loads bootstrap configuration from a .env file (if it exists)
// and environment variables. Only DATABASE_URL is required.
func LoadBootstrap(envFile string) (*BootstrapConfig, error) {
	dbURL, err := LoadDatabaseURL(envFile)
	if err != nil {
		return nil, err
	}

	// SECRET_KEY is the at-rest encryption master key. It is required: the server
	// must never fall back to a zero key or a key derived from another value, or
	// encrypted credentials would not survive a database dump. godotenv has
	// already loaded .env above, so dev sets it there. SECRET_KEY_FILE can name
	// a file holding it instead; the key is its content less one line ending.
	secretKey, secretKeyVar, err := envutil.LookupSecret(os.Getenv, "SECRET_KEY")
	if err != nil {
		return nil, err
	}
	if len(secretKey) < minSecretKeyLen {
		return nil, fmt.Errorf("%s is required (>=%d chars); generate one with: openssl rand -base64 48", secretKeyVar, minSecretKeyLen)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	jfPort := os.Getenv("JF_PORT")
	if jfPort == "" {
		jfPort = "8096"
	}

	absPort := os.Getenv("ABS_PORT")
	if absPort == "" {
		absPort = "13378"
	}

	mode := os.Getenv("MODE")
	if mode == "" {
		mode = "integrated"
	}
	// NODE_URL is a proxy's whole identity: it keys session bookkeeping and
	// selects the stream_nodes row whose acceleration overrides the proxy
	// adopts. A URL guessed from the listen port would give two proxies on the
	// same port one identity.
	// SILO_NODE_URL is the preferred spelling; NODE_URL still works.
	if mode == modeProxy && os.Getenv("SILO_NODE_URL") == "" && os.Getenv("NODE_URL") == "" {
		return nil, fmt.Errorf("MODE=%s needs SILO_NODE_URL (or NODE_URL), this node's own URL, which matches it to its entry on the main server", mode)
	}

	redisURL, redisURLVar, err := envutil.LookupSecret(os.Getenv, "REDIS_URL")
	if err != nil {
		return nil, err
	}

	return &BootstrapConfig{
		DatabaseURL: dbURL,
		RedisURL:    redisURL,
		Listen:      ":" + port,
		JFListen:    ":" + jfPort,
		ABSListen:   ":" + absPort,
		Mode:        mode,
		RedisURLVar: redisURLVar,
		SecretKey:   []byte(secretKey),
	}, nil
}
