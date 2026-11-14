package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig"
)

// SignatureState is the outcome of the release signature check run when a
// package was installed. See docs/architecture/plugin-trust.md#release-signatures.
type SignatureState string

const (
	// SignatureStateUnknown: not checked. Installations from before the
	// check, and installs whose check could not run (trust root or bundle
	// unreachable) while signatures were not enforced.
	SignatureStateUnknown SignatureState = "unknown"
	// SignatureStateVerified: the release bundle proved the policy the
	// plugin ID selects.
	SignatureStateVerified SignatureState = "verified"
	// SignatureStateUnsigned: no bundle to check, as for an upload, an
	// archive install, or a catalog entry without bundle_url.
	SignatureStateUnsigned SignatureState = "unsigned"
	// SignatureStateInvalid: a bundle was checked and does not prove the
	// policy.
	SignatureStateInvalid SignatureState = "invalid"
)

// Signature is what an installation records about its package's signature.
// SourceRepo and VerifiedAt are set only for a verified package.
type Signature struct {
	State      SignatureState
	SourceRepo *string
	VerifiedAt *time.Time
}

var unsignedSignature = Signature{State: SignatureStateUnsigned}

// maxSignatureBundleBytes caps a release's Sigstore bundle. A provenance
// bundle for a dozen binaries is a few tens of kilobytes.
const maxSignatureBundleBytes = 256 << 10

// tufCacheDirName is the TUF cache under the plugin cache root. Plugin
// directories are named by sanitizeFilesystemSegment, which never leaves an
// "@", so no plugin can claim this name.
const tufCacheDirName = "@sigstore-tuf"

// Admin-facing refusals. They name no URL or checksum; the cause goes to the
// log and the PackageError's Err.
const (
	signatureMissingMessage     = "This plugin's catalog entry has no release signature, so Silo couldn't confirm that the Silo release pipeline built it and didn't install it."
	signatureInvalidMessage     = "Silo couldn't confirm that the Silo release pipeline built this plugin, so it didn't install it."
	signatureIdentityMessage    = "The catalog entry for this plugin doesn't name its plugin ID and version, so Silo couldn't check its release signature and didn't install it."
	signatureTrustRootMessage   = "Silo couldn't load the Sigstore trust data it needs to check this plugin's release signature. Check that this server can reach tuf-repo-cdn.sigstore.dev, then try again."
	signatureBundleSizeMessage  = "This plugin's release signature is larger than Silo accepts, so Silo didn't install it."
	signatureBundleFetchSubject = "this plugin's release signature"
)

type bundleVerifier interface {
	Verify(ctx context.Context, bundleJSON []byte, sha256Digest []byte, policy pluginsig.Policy) (*pluginsig.Result, error)
}

// SignatureChecker applies the release signature policy for catalog installs.
// The catalog service and the installer share one, so a single Enforce
// setting decides both where a catalog entry is refused and where a
// downloaded binary is.
type SignatureChecker struct {
	verifier bundleVerifier
	enforce  bool
	now      func() time.Time
}

// SignatureCheckerOptions configures a SignatureChecker.
type SignatureCheckerOptions struct {
	// Enforce refuses a first-party plugin (officialPluginRepositories) whose
	// release signature is missing or fails the required policy, before its
	// binary runs. Off, the same checks run and their outcome is recorded,
	// and nothing is refused.
	Enforce bool
	// CacheRoot is the plugin cache root. The Sigstore TUF cache lives in it.
	CacheRoot string
}

// NewSignatureChecker returns a checker that loads the Sigstore trust root
// through TUF on first use, fetching through the netguard package-fetch
// transport.
func NewSignatureChecker(opts SignatureCheckerOptions) *SignatureChecker {
	trust := pluginsig.NewTUFTrustRoot(pluginsig.TUFTrustRootOptions{
		CacheDir:  filepath.Join(opts.CacheRoot, tufCacheDirName),
		Transport: newPackageFetchClient(defaultPackageFetchTimeouts).Transport,
	})
	return &SignatureChecker{
		verifier: pluginsig.NewVerifier(trust.TrustedMaterial),
		enforce:  opts.Enforce,
		now:      time.Now,
	}
}

// signaturePolicy returns the policy pluginID selects: the required policy
// for a first-party plugin ID, whichever catalog lists it, and the advisory
// one for any other. An empty ID gets the required policy, since it can't be
// ruled out as first-party.
func signaturePolicy(pluginID, version, catalogRepoURL string) (pluginsig.Policy, bool) {
	if repo, ok := officialPluginRepository(pluginID); ok || pluginID == "" {
		return pluginsig.OfficialPolicy(repo, version), true
	}
	return pluginsig.AdvisoryPolicy(normalizeRepoURL(catalogRepoURL), version), false
}

// normalizeRepoURL drops the trailing slash or .git a catalog's repo_url may
// carry; a certificate's Source Repository URI has neither.
func normalizeRepoURL(raw string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(raw), "/"), ".git")
}

// checkCatalogPackage refuses, while enforcing, a first-party catalog entry
// that can't carry a release signature: a legacy archive_url package, or one
// without bundle_url. Any other entry passes; its signature is checked after
// download.
func (c *SignatureChecker) checkCatalogPackage(ctx context.Context, pluginID string, legacyArchive bool, bundleURL string) error {
	if c == nil || !c.enforce {
		return nil
	}
	if _, official := officialPluginRepository(pluginID); !official {
		return nil
	}
	switch {
	case legacyArchive:
		return c.refuse(ctx, pluginID, "", packageError(fmt.Errorf("first-party plugin %s is listed as a legacy archive", pluginID), signatureMissingMessage))
	case strings.TrimSpace(bundleURL) == "":
		return c.refuse(ctx, pluginID, "", packageError(fmt.Errorf("first-party plugin %s is listed without bundle_url", pluginID), signatureMissingMessage))
	}
	return nil
}

// checkBinary checks a downloaded catalog binary's release signature before
// the binary is run. fetch downloads the bundle. It returns what to record,
// or, while enforcing the required policy, the refusal.
func (c *SignatureChecker) checkBinary(
	ctx context.Context,
	req InstallBinaryRequest,
	digest []byte,
	fetch func(ctx context.Context, url string) ([]byte, error),
) (Signature, error) {
	policy, required := signaturePolicy(req.PluginID, req.Version, req.SourceRepoURL)
	refusing := required && c.enforce
	if required && (req.PluginID == "" || req.Version == "") {
		cause := fmt.Errorf("catalog install of %q version %q names no plugin ID or version", req.PluginID, req.Version)
		return c.outcome(ctx, req, refusing, SignatureStateInvalid, packageError(cause, signatureIdentityMessage))
	}
	if req.BundleURLErr != nil {
		// The resolution error quotes the catalog's URL, which could carry a
		// credential in its query, so only its redacted text is logged.
		cause := fmt.Errorf("bundle_url of %s %s doesn't resolve: %s", req.PluginID, req.Version,
			logredact.SanitizeText(req.BundleURLErr.Error()))
		return c.outcome(ctx, req, refusing, SignatureStateInvalid, packageError(cause, signatureInvalidMessage))
	}
	if strings.TrimSpace(req.BundleURL) == "" {
		if !required {
			return unsignedSignature, nil
		}
		cause := fmt.Errorf("first-party plugin %s %s has no bundle_url", req.PluginID, req.Version)
		return c.outcome(ctx, req, refusing, SignatureStateUnsigned, packageError(cause, signatureMissingMessage))
	}

	bundleJSON, err := fetch(ctx, req.BundleURL)
	if err != nil {
		if ctx.Err() != nil {
			return Signature{}, err
		}
		state := SignatureStateInvalid
		if packageErr, ok := AsPackageError(err); ok && packageErr.Transient {
			state = SignatureStateUnknown
		}
		return c.outcome(ctx, req, refusing, state, err)
	}

	result, err := c.verifier.Verify(ctx, bundleJSON, digest, policy)
	switch {
	case errors.Is(err, pluginsig.ErrTrustRootUnavailable):
		return c.outcome(ctx, req, refusing, SignatureStateUnknown, transientPackageError(err, signatureTrustRootMessage))
	case err != nil:
		return c.outcome(ctx, req, refusing, SignatureStateInvalid, packageError(err, signatureInvalidMessage))
	}
	verifiedAt := c.now().UTC()
	slog.InfoContext(ctx, "verified plugin release signature", "component", "plugins",
		"plugin_id", req.PluginID, "version", req.Version, "required", required,
		"source_repository", result.SourceRepository, "source_ref", result.SourceRef,
		"source_digest", result.SourceDigest, "signer", result.SignerIdentity)
	return Signature{State: SignatureStateVerified, SourceRepo: &result.SourceRepository, VerifiedAt: &verifiedAt}, nil
}

// outcome logs a signature check that did not verify, and either refuses
// with packageErr or records state.
func (c *SignatureChecker) outcome(ctx context.Context, req InstallBinaryRequest, refusing bool, state SignatureState, packageErr error) (Signature, error) {
	if refusing {
		return Signature{}, c.refuse(ctx, req.PluginID, req.Version, packageErr)
	}
	// The PackageError's Message is the refusal an admin would read, and
	// nothing was refused here, so log only the cause.
	cause := packageErr
	if refusal, ok := AsPackageError(packageErr); ok && refusal.Err != nil {
		cause = refusal.Err
	}
	slog.WarnContext(ctx, "plugin release signature not verified", "component", "plugins",
		"plugin_id", req.PluginID, "version", req.Version, "recorded_state", string(state), "error", cause)
	return Signature{State: state}, nil
}

func (c *SignatureChecker) refuse(ctx context.Context, pluginID, version string, packageErr error) error {
	slog.WarnContext(ctx, "refused plugin without a valid release signature", "component", "plugins",
		"plugin_id", pluginID, "version", version, "error", packageErr)
	return packageErr
}

// fetchSignatureBundle downloads a release bundle with the package-fetch
// rules and a size cap.
func fetchSignatureBundle(ctx context.Context, client *http.Client, bundleURL string) ([]byte, error) {
	data, err := fetchPackageResourceLimited(ctx, client, bundleURL, signatureBundleFetchSubject, maxSignatureBundleBytes)
	if errors.Is(err, errPackageResourceTooLarge) {
		return nil, packageError(err, signatureBundleSizeMessage)
	}
	return data, err
}
