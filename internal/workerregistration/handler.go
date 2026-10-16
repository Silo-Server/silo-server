package workerregistration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

const enrollmentPath = "/api/internal/workers/enroll"
const domainEnrollmentRequest = "silo/worker-enrollment-request/v1"
const domainBindingGrant = "silo/worker-binding/v1"

type EnrollmentRepository interface {
	Redeem(context.Context, Enrollment, ed25519.PublicKey) error
}
type Options struct {
	Repository EnrollmentRepository
	Origin     string
	PrivateKey ed25519.PrivateKey
	AllowHTTP  bool
}
type enrollmentRequest struct {
	Enrollment    Enrollment        `json:"enrollment"`
	NodePublicKey ed25519.PublicKey `json:"node_public_key"`
}
type PublicBinding struct {
	Version       int               `json:"version"`
	NodeID        int               `json:"node_id"`
	CoreURL       string            `json:"core_url"`
	CorePublicKey ed25519.PublicKey `json:"core_public_key"`
	NodeURL       string            `json:"node_url"`
	NodePublicKey ed25519.PublicKey `json:"node_public_key"`
	AllowHTTP     bool              `json:"allow_http"`
}

type enrollmentHandler struct {
	options   Options
	verifier  *workeridentity.Verifier
	proofMu   sync.Mutex
	proofNode int
	proofKey  ed25519.PublicKey
}

func NewHandler(options Options) (http.Handler, error) {
	origin, err := workeridentity.ValidateOrigin(options.Origin, options.AllowHTTP)
	if err != nil || origin != options.Origin || options.Repository == nil || len(options.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid core enrollment configuration")
	}
	probe := workeridentity.Grant{Version: 1, Domain: domainBindingGrant, Audience: origin, NodeID: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(), Body: json.RawMessage(`{}`)}
	if _, err = workeridentity.EncodeGrant(probe, options.PrivateKey); err != nil {
		return nil, err
	}
	options.PrivateKey = append(ed25519.PrivateKey(nil), options.PrivateKey...)
	h := &enrollmentHandler{options: options}
	h.verifier = &workeridentity.Verifier{Origin: origin, Domain: domainEnrollmentRequest, AllowHTTP: options.AllowHTTP, MaxNonces: 4096, ResolveKey: func(id int) (ed25519.PublicKey, error) {
		if id != h.proofNode {
			return nil, errors.New("enrollment proof node mismatch")
		}
		return h.proofKey, nil
	}}
	return h, nil
}

func (h *enrollmentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != enrollmentPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workeridentity.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid enrollment request", http.StatusBadRequest)
		return
	}
	var request enrollmentRequest
	if err = decodeStrict(bytes.NewReader(data), &request); err != nil || request.Enrollment.Validate() != nil || workeridentity.ValidatePublicKey(request.NodePublicKey) != nil {
		http.Error(w, "invalid enrollment request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	// The enrollment key is supplied in the signed body. Serialize the
	// temporary resolver while retaining one replay cache across all requests.
	h.proofMu.Lock()
	h.proofNode = request.Enrollment.NodeID
	h.proofKey = request.NodePublicKey
	err = h.verifier.Verify(r, request.Enrollment.NodeID)
	h.proofNode = 0
	h.proofKey = nil
	h.proofMu.Unlock()
	if err != nil {
		http.Error(w, "invalid node key proof", http.StatusUnauthorized)
		return
	}
	e := request.Enrollment
	if e.CoreURL != h.options.Origin || !bytes.Equal(e.CorePublicKey, ed25519.PublicKey(h.options.PrivateKey[ed25519.SeedSize:])) || time.Now().Unix() >= e.ExpiresAt {
		http.Error(w, "enrollment context expired or changed", http.StatusForbidden)
		return
	}
	// The operator's complete ticket digest binds the node HTTP policy. The
	// core listener's scheme is independent: HTTPS cores can enroll explicitly
	// approved HTTP workers while retaining standard core TLS verification.
	if err = h.options.Repository.Redeem(r.Context(), e, request.NodePublicKey); err != nil {
		http.Error(w, "enrollment unavailable or consumed", http.StatusConflict)
		return
	}
	public := PublicBinding{Version: 1, NodeID: e.NodeID, CoreURL: e.CoreURL, CorePublicKey: e.CorePublicKey, NodeURL: e.NodeURL, NodePublicKey: request.NodePublicKey, AllowHTTP: e.AllowHTTP}
	body, _ := json.Marshal(public)
	grant, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: domainBindingGrant, Audience: e.NodeURL, NodeID: e.NodeID, ExpiresAt: time.Now().Add(30 * time.Second).Unix(), Body: body}, h.options.PrivateKey)
	if err != nil {
		http.Error(w, "binding unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"grant": grant}); err != nil {
		slog.WarnContext(r.Context(), "write worker enrollment grant", "error", err)
	}
}

// Enroll keeps the node private key local. A consumed ticket is never retried
// automatically: a lost response requires a fresh operator-issued ticket.
func Enroll(ctx context.Context, e Enrollment, key ed25519.PrivateKey, client *http.Client) (workeridentity.Binding, error) {
	var binding workeridentity.Binding
	if err := e.Validate(); err != nil {
		return binding, err
	}
	if time.Now().Unix() >= e.ExpiresAt {
		return binding, errors.New("enrollment ticket expired")
	}
	binding = workeridentity.Binding{Version: 1, NodeID: e.NodeID, CoreURL: e.CoreURL, CorePublicKey: e.CorePublicKey, NodePrivateKey: key, NodeURL: e.NodeURL, AllowHTTP: e.AllowHTTP}
	if err := binding.Validate(); err != nil {
		return workeridentity.Binding{}, err
	}
	data, _ := json.Marshal(enrollmentRequest{Enrollment: e, NodePublicKey: ed25519.PublicKey(key[ed25519.SeedSize:])})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.CoreURL+enrollmentPath, bytes.NewReader(data))
	if err != nil {
		return workeridentity.Binding{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err = workeridentity.Sign(req, e.CoreURL, domainEnrollmentRequest, key, e.NodeID, e.AllowHTTP); err != nil {
		return workeridentity.Binding{}, err
	}
	if client == nil {
		client = workeridentity.NewHTTPClient(e.AllowHTTP, 10*time.Second)
		defer client.CloseIdleConnections()
	}
	response, err := client.Do(req)
	if err != nil {
		return workeridentity.Binding{}, errors.New("enrollment request failed; obtain a fresh ticket before retrying")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return workeridentity.Binding{}, errors.New("enrollment rejected; obtain a fresh ticket before retrying")
	}
	var envelope struct {
		Grant string `json:"grant"`
	}
	if err = decodeStrict(response.Body, &envelope); err != nil {
		return workeridentity.Binding{}, errors.New("invalid enrollment reply")
	}
	grant, err := workeridentity.DecodeGrant(envelope.Grant, domainBindingGrant, e.NodeURL, e.CorePublicKey, time.Now())
	if err != nil || grant.NodeID != e.NodeID || grant.WorkID != "" || grant.Generation != 0 {
		return workeridentity.Binding{}, errors.New("invalid signed enrollment binding")
	}
	var public PublicBinding
	if err = decodeStrict(bytes.NewReader(grant.Body), &public); err != nil {
		return workeridentity.Binding{}, errors.New("invalid public enrollment binding")
	}
	if public.Version != 1 || public.NodeID != e.NodeID || public.CoreURL != e.CoreURL || public.NodeURL != e.NodeURL || public.AllowHTTP != e.AllowHTTP || !bytes.Equal(public.CorePublicKey, e.CorePublicKey) || !bytes.Equal(public.NodePublicKey, ed25519.PublicKey(key[ed25519.SeedSize:])) {
		return workeridentity.Binding{}, errors.New("enrollment binding scope mismatch")
	}
	return binding, nil
}

func SaveEnrollment(path string, e Enrollment) error {
	if err := e.Validate(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(e, "", "  ")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // The operator chooses the ticket path; exclusive creation and private permissions prevent replacement.
	if err != nil {
		return err
	}
	good := false
	defer func() {
		_ = file.Close()
		if !good {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	good = true
	return nil
}
func LoadEnrollment(path string) (Enrollment, error) {
	var e Enrollment
	info, err := os.Lstat(path)
	if err != nil {
		return e, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return e, errors.New("enrollment ticket requires a private regular file")
	}
	file, err := os.Open(path) //nolint:gosec // The operator chooses the ticket path; Lstat and SameFile fence the private regular file opened below.
	if err != nil {
		return e, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return e, err
	}
	if !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return e, errors.New("enrollment ticket changed while opening")
	}
	return DecodeEnrollment(file)
}
func DecodeEnrollment(r io.Reader) (Enrollment, error) {
	var e Enrollment
	if err := decodeStrict(r, &e); err != nil {
		return e, errors.New("invalid enrollment ticket JSON")
	}
	if err := e.Validate(); err != nil {
		return Enrollment{}, err
	}
	return e, nil
}
func decodeStrict(r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, workeridentity.MaxBodyBytes+1))
	if err != nil || len(data) > workeridentity.MaxBodyBytes {
		return errors.New("invalid enrollment JSON size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing enrollment JSON")
	}
	return nil
}
