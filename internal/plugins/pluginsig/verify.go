// Package pluginsig verifies the Sigstore bundle a plugin release publishes
// beside its binaries: one GitHub artifact attestation (SLSA provenance)
// whose subjects are the release's binaries, signed with a public-good Fulcio
// certificate and logged in Rekor. See
// docs/architecture/plugin-trust.md#release-signatures.
package pluginsig

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	// GitHubActionsIssuer is the OIDC issuer of every GitHub Actions
	// certificate.
	GitHubActionsIssuer = "https://token.actions.githubusercontent.com"
	// OfficialSignerIdentity is the certificate SAN of Silo's reusable
	// release workflow. Callers reference it at @main, so the SAN names the
	// protected branch rather than a commit that might not be on it.
	OfficialSignerIdentity = "https://github.com/Silo-Server/.github/.github/workflows/plugin-release.yml@refs/heads/main"
	// GitHubHostedRunner is the runner environment a GitHub-hosted runner
	// reports; a self-hosted runner reports "self-hosted".
	GitHubHostedRunner = "github-hosted"
	// SLSAProvenanceV1 is the predicate actions/attest-build-provenance
	// writes.
	SLSAProvenanceV1 = "https://slsa.dev/provenance/v1"
)

// anyGitHubWorkflow matches the SAN of any GitHub Actions workflow. sigstore-go
// does not anchor SAN regexes, so the anchor is written out.
const anyGitHubWorkflow = `^https://github\.com/`

// ErrTrustRootUnavailable marks a verification that could not start because
// the Sigstore trust root could not be loaded: TUF was unreachable and the
// cached metadata had expired, or no cache existed yet. Nothing about the
// bundle was decided.
var ErrTrustRootUnavailable = errors.New("sigstore trust root unavailable")

// Policy is what a release's signing certificate and attestation must show.
// Every field that names a value is compared exactly; an empty SourceRepository
// or SourceRef is refused rather than skipped.
type Policy struct {
	// SignerIdentity is the exact certificate SAN required. Empty accepts any
	// GitHub Actions workflow.
	SignerIdentity string
	// SourceRepository is the exact Source Repository URI the certificate
	// must carry, such as https://github.com/Silo-Server/silo-plugin-metadata-tmdb.
	SourceRepository string
	// SourceRef is the exact Source Repository Ref, such as refs/tags/v1.2.3.
	SourceRef string
	// RequireGitHubHosted refuses a certificate issued to a self-hosted runner.
	RequireGitHubHosted bool
	// RequireSLSAProvenance refuses an attestation with another predicate.
	RequireSLSAProvenance bool
}

// TagRef is the Source Repository Ref a release of version is signed from.
func TagRef(version string) string { return "refs/tags/v" + version }

// OfficialPolicy is the required policy for a first-party plugin: built by
// Silo's reusable release workflow on a GitHub-hosted runner, from the tag of
// version in repository.
func OfficialPolicy(repository, version string) Policy {
	return Policy{
		SignerIdentity:        OfficialSignerIdentity,
		SourceRepository:      repository,
		SourceRef:             TagRef(version),
		RequireGitHubHosted:   true,
		RequireSLSAProvenance: true,
	}
}

// AdvisoryPolicy checks a third-party release: any GitHub Actions workflow in
// repository, from the tag of version.
func AdvisoryPolicy(repository, version string) Policy {
	return Policy{SourceRepository: repository, SourceRef: TagRef(version)}
}

func (p Policy) validate() error {
	if strings.TrimSpace(p.SourceRepository) == "" {
		return errors.New("policy names no source repository")
	}
	if strings.TrimSpace(p.SourceRef) == "" || p.SourceRef == TagRef("") {
		return errors.New("policy names no source ref")
	}
	return nil
}

// Result is what a verified bundle proved.
type Result struct {
	SourceRepository string
	SourceRef        string
	SourceDigest     string
	SignerIdentity   string
}

// TrustedMaterialFunc returns the Sigstore trust root to verify against.
type TrustedMaterialFunc func(ctx context.Context) (root.TrustedMaterial, error)

// Verifier checks release bundles against a trust root.
type Verifier struct {
	trusted TrustedMaterialFunc
}

// NewVerifier returns a Verifier that loads its trust root from trusted on
// each verification.
func NewVerifier(trusted TrustedMaterialFunc) *Verifier {
	return &Verifier{trusted: trusted}
}

// Verify checks bundleJSON against policy and requires the attestation's
// subjects to include sha256Digest. It requires one signed certificate
// timestamp, one transparency log entry and one observer timestamp. An error
// wrapping ErrTrustRootUnavailable decided nothing about the bundle; any other
// error means it does not prove what policy asks.
func (v *Verifier) Verify(ctx context.Context, bundleJSON []byte, sha256Digest []byte, policy Policy) (result *Result, err error) {
	// The bundle comes from whoever publishes the catalog. sigstore-go before
	// v1.3.0 can panic on some malformed transparency log entries (fixed in
	// sigstore/sigstore-go#658); a bundle that crashes the parser is invalid,
	// not a reason to take the server down.
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("verify bundle: sigstore-go panicked: %v", r)
		}
	}()
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if len(sha256Digest) != 32 {
		return nil, fmt.Errorf("artifact digest is %d bytes, want a SHA-256", len(sha256Digest))
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	trusted, err := v.trusted(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTrustRootUnavailable, err)
	}
	verifier, err := verify.NewVerifier(trusted,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("build verifier: %w", err)
	}
	identity, err := policy.certificateIdentity()
	if err != nil {
		return nil, err
	}
	verified, err := verifier.Verify(&b, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", sha256Digest),
		verify.WithCertificateIdentity(identity),
	))
	if err != nil {
		return nil, fmt.Errorf("verify bundle: %w", err)
	}
	return policy.check(verified, sha256Digest)
}

func (p Policy) certificateIdentity() (verify.CertificateIdentity, error) {
	san, err := verify.NewSANMatcher(p.SignerIdentity, "")
	if p.SignerIdentity == "" {
		san, err = verify.NewSANMatcher("", anyGitHubWorkflow)
	}
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuer, err := verify.NewIssuerMatcher(GitHubActionsIssuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	extensions := certificate.Extensions{
		SourceRepositoryURI: p.SourceRepository,
		SourceRepositoryRef: p.SourceRef,
	}
	if p.RequireGitHubHosted {
		extensions.RunnerEnvironment = GitHubHostedRunner
	}
	return verify.NewCertificateIdentity(san, issuer, extensions)
}

// check repeats the identity comparison on the verified result.
// sigstore-go skips an extension whose expected value is empty and leaves SAN
// regexes unanchored, so a mistake in building the identity would otherwise
// pass silently.
func (p Policy) check(result *verify.VerificationResult, digest []byte) (*Result, error) {
	if result == nil || result.Signature == nil || result.Signature.Certificate == nil {
		return nil, errors.New("bundle is not signed with a certificate")
	}
	cert := result.Signature.Certificate
	switch {
	case cert.Issuer != GitHubActionsIssuer:
		return nil, fmt.Errorf("certificate issuer %q, want %q", cert.Issuer, GitHubActionsIssuer)
	case p.SignerIdentity != "" && cert.SubjectAlternativeName != p.SignerIdentity:
		return nil, fmt.Errorf("certificate SAN %q, want %q", cert.SubjectAlternativeName, p.SignerIdentity)
	case p.SignerIdentity == "" && !strings.HasPrefix(cert.SubjectAlternativeName, "https://github.com/"):
		return nil, fmt.Errorf("certificate SAN %q is not a GitHub workflow", cert.SubjectAlternativeName)
	case cert.SourceRepositoryURI != p.SourceRepository:
		return nil, fmt.Errorf("certificate source repository %q, want %q", cert.SourceRepositoryURI, p.SourceRepository)
	case cert.SourceRepositoryRef != p.SourceRef:
		return nil, fmt.Errorf("certificate source ref %q, want %q", cert.SourceRepositoryRef, p.SourceRef)
	case p.RequireGitHubHosted && cert.RunnerEnvironment != GitHubHostedRunner:
		return nil, fmt.Errorf("certificate runner environment %q, want %q", cert.RunnerEnvironment, GitHubHostedRunner)
	}
	statement := result.Statement
	if statement == nil {
		return nil, errors.New("bundle carries no in-toto statement")
	}
	if p.RequireSLSAProvenance && statement.GetPredicateType() != SLSAProvenanceV1 {
		return nil, fmt.Errorf("attestation predicate %q, want %q", statement.GetPredicateType(), SLSAProvenanceV1)
	}
	if !hasSubjectDigest(statement.GetSubject(), digest) {
		return nil, errors.New("attestation subjects do not include the artifact digest")
	}
	return &Result{
		SourceRepository: cert.SourceRepositoryURI,
		SourceRef:        cert.SourceRepositoryRef,
		SourceDigest:     cert.SourceRepositoryDigest,
		SignerIdentity:   cert.SubjectAlternativeName,
	}, nil
}

type subjectDigests interface{ GetDigest() map[string]string }

func hasSubjectDigest[S subjectDigests](subjects []S, digest []byte) bool {
	for _, subject := range subjects {
		got, err := hex.DecodeString(subject.GetDigest()["sha256"])
		if err == nil && bytes.Equal(got, digest) {
			return true
		}
	}
	return false
}
