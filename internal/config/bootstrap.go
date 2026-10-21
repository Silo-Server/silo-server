package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// minSecretKeyLen is the minimum acceptable SECRET_KEY length (in characters).
// It mirrors secret.MinMasterKeyLen; the package is not imported here to keep
// the bootstrap loader dependency-free, so the two constants must stay in sync.
const minSecretKeyLen = 32

const (
	bootstrapModeIntegrated = "integrated"
	bootstrapModeTranscode  = "transcode"
)

// BootstrapConfig holds the minimal config needed before database connection.
type BootstrapConfig struct {
	DatabaseURL       string
	RedisURL          string // optional override; empty means use DB setting
	Listen            string
	JFListen          string
	Mode              string
	TranscodeMaxJobs  int  // operator-owned worker ceiling; 0 means unlimited
	EnrollmentEnabled bool // core operator permits sending bootstrap secrets to workers
	EnrollmentToken   string
	BindingFile       string
	NodeURL           string
	NodeName          string
	// SecretKey is the master key (raw SECRET_KEY env value) from which the
	// at-rest credential cipher derives its data key. It lives outside Postgres
	// so encrypted secrets survive a full database compromise/dump.
	SecretKey []byte
}

// LoadDatabaseURL loads DATABASE_URL from a .env file (if it exists) and
// environment variables, for maintenance commands that only reach the
// database and must not need the server's other bootstrap secrets.
func LoadDatabaseURL(envFile string) (string, error) {
	if envFile != "" {
		_ = godotenv.Load(envFile)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return "", errors.New("DATABASE_URL is required (set in .env or environment)")
	}
	return dbURL, nil
}

// LoadBootstrap loads bootstrap configuration from a .env file (if it exists)
// and environment variables. Customer DATABASE_URL and SECRET_KEY are required.
func LoadBootstrap(envFile string) (*BootstrapConfig, error) {
	return LoadStartupBootstrap(envFile, false)
}

// LoadStartupBootstrap permits credential-free startup only for an explicitly
// enrolled transcode worker. Maintenance and legacy loaders stay strict.
func LoadStartupBootstrap(envFile string, allowEnrollment bool) (*BootstrapConfig, error) {
	if envFile != "" {
		_ = godotenv.Load(envFile)
	}
	dbURL := os.Getenv("DATABASE_URL")
	secretKey := os.Getenv("SECRET_KEY")
	mode := os.Getenv("MODE")
	if mode == "" {
		mode = bootstrapModeIntegrated
	}
	token, bindingFile := os.Getenv("SILO_NODE_ENROLLMENT_TOKEN"), os.Getenv("SILO_NODE_BINDING_FILE")
	canEnroll := allowEnrollment && mode == bootstrapModeTranscode && (token != "" || bindingFile != "")
	if token != "" && (len(token) < 32 || len(token) > 512) {
		return nil, errors.New("SILO_NODE_ENROLLMENT_TOKEN must contain 32 to 512 characters")
	}
	if !canEnroll || dbURL != "" || secretKey != "" {
		if dbURL == "" {
			return nil, errors.New("DATABASE_URL is required (set in .env or environment)")
		}
		if len(secretKey) < minSecretKeyLen {
			return nil, fmt.Errorf("SECRET_KEY is required (>=%d chars); generate one with: openssl rand -base64 48", minSecretKeyLen)
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	jfPort := os.Getenv("JF_PORT")
	if jfPort == "" {
		jfPort = "8096"
	}

	redisURL := os.Getenv("REDIS_URL")
	enrollmentEnabled := false
	if value := os.Getenv("SILO_NODE_ENROLLMENT_ENABLED"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("SILO_NODE_ENROLLMENT_ENABLED must be a boolean")
		}
		enrollmentEnabled = parsed
	}
	maxJobs := 0
	if value := os.Getenv("SILO_TRANSCODE_MAX_JOBS"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 0 {
			return nil, errors.New("SILO_TRANSCODE_MAX_JOBS must be an integer between 0 and 2147483647")
		}
		maxJobs = int(parsed)
	}

	return &BootstrapConfig{
		DatabaseURL:       dbURL,
		RedisURL:          redisURL,
		Listen:            ":" + port,
		JFListen:          ":" + jfPort,
		Mode:              mode,
		TranscodeMaxJobs:  maxJobs,
		EnrollmentEnabled: enrollmentEnabled,
		EnrollmentToken:   token,
		BindingFile:       bindingFile,
		NodeURL:           os.Getenv("NODE_URL"),
		NodeName:          os.Getenv("NODE_NAME"),
		SecretKey:         []byte(secretKey),
	}, nil
}
