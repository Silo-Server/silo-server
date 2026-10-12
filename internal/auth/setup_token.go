package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/streamlocation"
)

// First-run setup is open to a client on the local network. Anyone else must
// present a setup token: each API process that boots while no account exists
// prints one to its log and stores its SHA-256 in setup_tokens, so whichever
// replica answers the setup request can check it. Completing setup deletes
// every stored token, and a process deletes its own on graceful shutdown. A
// process that dies without shutting down leaves its token valid until setup
// completes.
var (
	// ErrSetupTokenRequired means a client outside the local network tried
	// setup without a token.
	ErrSetupTokenRequired = errors.New("setup token required")
	// ErrSetupTokenInvalid means the presented token matches no live token.
	ErrSetupTokenInvalid = errors.New("setup token invalid")
)

// SetupTokenRequired reports whether a setup request carried by ctx must
// present a setup token: it does unless the resolved client address is on
// the local network. An absent or unparsable address, or a request that came
// through a network-access provider, needs the token.
func SetupTokenRequired(ctx context.Context) bool {
	return streamlocation.FromContext(ctx) != streamlocation.Local
}

// setupCrossOrigin recognizes browser requests that another site started.
var setupCrossOrigin = http.NewCrossOriginProtection()

// SetupAccessFor decides how the setup request r qualifies. ctx carries the
// resolved client address; when forwarding headers came from a peer the
// resolver does not trust, that address is the proxy's and the request is not
// local. A browser request (one with Origin or
// Sec-Fetch-Site) counts as local only when another site did not start it
// and the browser addressed a host only the local network can reach (see
// localSetupHost), so a page can use a browser on the local network neither
// across sites nor by rebinding its own domain to the server's address. A
// missing request never counts as local.
func SetupAccessFor(ctx context.Context, r *http.Request, token string) SetupAccess {
	if r == nil || SetupTokenRequired(ctx) || forwardedByUntrustedPeer(ctx, r) {
		return SetupAccess{Token: token}
	}
	if r.Header.Get("Origin") == "" && r.Header.Get("Sec-Fetch-Site") == "" {
		return SetupAccess{Local: true, Token: token}
	}
	// Behind a trusted proxy that rewrites Host, the client's host is in
	// X-Forwarded-Host.
	host := clientip.RequestHost(r)
	if !localSetupHost(host) {
		return SetupAccess{Token: token}
	}
	checked := r
	if host != r.Host {
		checked = r.WithContext(r.Context())
		checked.Host = host
	}
	return SetupAccess{Local: setupCrossOrigin.Check(checked) == nil, Token: token}
}

// forwardedByUntrustedPeer reports whether r carries forwarding headers that
// the client-IP resolver ignored because the peer that sent them is not a
// trusted proxy, for example a reverse proxy on an IPv6 ULA address. The
// resolved address is then the proxy's own, which says nothing about where
// the client is.
func forwardedByUntrustedPeer(ctx context.Context, r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("X-Real-IP") == "" && r.Header.Get("Forwarded") == "" {
		return false
	}
	peer := clientip.PeerFromContext(ctx)
	if peer == "" {
		peer = r.RemoteAddr
	}
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	ip := net.ParseIP(peer)
	return ip != nil && ip.String() == clientip.FromContext(ctx)
}

// localSetupDomains are special-use or conventional local suffixes that no
// public DNS delegates, so no outside site can rebind a name under them.
var localSetupDomains = []string{".local", ".localhost", ".localdomain", ".home.arpa", ".internal", ".lan"}

// localSetupHost reports whether host[:port] names the server the way only a
// client on the local network can: an IP address, localhost, a single-label
// name, or a name under localSetupDomains. Any other name could be a public
// domain whose owner points it at the server's address (DNS rebinding).
func localSetupHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range localSetupDomains {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// SetupAccess is how a caller qualifies for first-run setup.
type SetupAccess struct {
	// Local is true when the request came from the local network.
	Local bool
	// Token is the setup token the caller presented, if any.
	Token string
}

func setupTokenHash(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(token))))
}

// IssueSetupToken stores a new setup token when no account exists yet and
// returns it so the caller can print it; it returns "" once setup is done.
// It stores the token under the setup lock, so a token is never stored after
// setup commits. A server that is already set up answers from one read
// without waiting on the lock.
func (r *UserRepository) IssueSetupToken(ctx context.Context) (string, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
		return "", fmt.Errorf("checking for accounts: %w", err)
	}
	if exists {
		return "", nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("beginning setup token issue: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := lockEmptyInitialSetup(ctx, tx); err != nil {
		if errors.Is(err, ErrSetupAlreadyComplete) {
			return "", nil
		}
		return "", err
	}
	token := rand.Text()
	hash := setupTokenHash(token)
	if _, err := tx.Exec(ctx, "INSERT INTO setup_tokens (token_hash) VALUES ($1)", hash[:]); err != nil {
		return "", fmt.Errorf("storing setup token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("committing setup token: %w", err)
	}
	return token, nil
}

// RevokeSetupToken deletes one setup token, for a process that is shutting
// down and takes the token it printed with it.
func (r *UserRepository) RevokeSetupToken(ctx context.Context, token string) error {
	hash := setupTokenHash(token)
	if _, err := r.pool.Exec(ctx, "DELETE FROM setup_tokens WHERE token_hash = $1", hash[:]); err != nil {
		return fmt.Errorf("revoking setup token: %w", err)
	}
	return nil
}

// checkSetupAccess runs inside the setup transaction. Every stored hash is
// compared in constant time so the answer does not depend on which row, if
// any, matched.
func checkSetupAccess(ctx context.Context, tx pgx.Tx, access SetupAccess) error {
	if access.Local {
		return nil
	}
	if strings.TrimSpace(access.Token) == "" {
		return ErrSetupTokenRequired
	}
	presented := setupTokenHash(access.Token)
	rows, err := tx.Query(ctx, "SELECT token_hash FROM setup_tokens")
	if err != nil {
		return fmt.Errorf("reading setup tokens: %w", err)
	}
	stored, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return fmt.Errorf("reading setup tokens: %w", err)
	}
	match := 0
	for _, hash := range stored {
		match |= subtle.ConstantTimeCompare(hash, presented[:])
	}
	if match != 1 {
		return ErrSetupTokenInvalid
	}
	return nil
}
