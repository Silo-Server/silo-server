package pluginsig_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig"
	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig/pluginsigtest"
)

const (
	tmdbRepo    = "https://github.com/Silo-Server/silo-plugin-metadata-tmdb"
	tmdbVersion = "1.2.3"
)

// Every bundle here goes through bundle parsing and sigstore-go's verifier
// against a trust root whose Fulcio leaf carries the GitHub extensions, an
// embedded SCT, and a Rekor entry with an inclusion proof and promise, as an
// actions/attest-build-provenance bundle does.
func TestVerifyOfficialPolicy(t *testing.T) {
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("plugin-linux-amd64")
	digest := pluginsigtest.Digest(binary)
	subjects := map[string][]byte{
		"plugin-linux-amd64": digest,
		"plugin-linux-arm64": pluginsigtest.Digest([]byte("plugin-linux-arm64")),
	}
	official := func(edit func(*pluginsigtest.Release)) pluginsigtest.Release {
		r := pluginsigtest.OfficialRelease(tmdbRepo, tmdbVersion, subjects)
		if edit != nil {
			edit(&r)
		}
		return r
	}

	for _, tc := range []struct {
		name    string
		release pluginsigtest.Release
		wantErr bool
	}{
		{name: "official release", release: official(nil)},
		{name: "workflow pinned to a commit", wantErr: true, release: official(func(r *pluginsigtest.Release) {
			r.SAN = "https://github.com/Silo-Server/.github/.github/workflows/plugin-release.yml@0123456789abcdef0123456789abcdef01234567"
		})},
		{name: "workflow on another branch", wantErr: true, release: official(func(r *pluginsigtest.Release) {
			r.SAN = "https://github.com/Silo-Server/.github/.github/workflows/plugin-release.yml@refs/heads/main-test"
		})},
		{name: "plugin repo's own workflow", wantErr: true, release: official(func(r *pluginsigtest.Release) {
			r.SAN = tmdbRepo + "/.github/workflows/release.yml@refs/tags/v" + tmdbVersion
		})},
		{name: "another issuer", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.Issuer = "https://accounts.example.test" })},
		{name: "another plugin's repo", wantErr: true, release: official(func(r *pluginsigtest.Release) {
			r.SourceRepository = "https://github.com/Silo-Server/silo-plugin-metadata-tvdb"
		})},
		{name: "another tag", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.SourceRef = "refs/tags/v1.2.2" })},
		{name: "branch instead of tag", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.SourceRef = "refs/heads/main" })},
		{name: "self-hosted runner", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.RunnerEnvironment = "self-hosted" })},
		{name: "digest not attested", wantErr: true, release: official(func(r *pluginsigtest.Release) {
			r.Subjects = map[string][]byte{"plugin-linux-arm64": subjects["plugin-linux-arm64"]}
		})},
		{name: "not provenance", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.PredicateType = "https://spdx.dev/Document" })},
		{name: "logged after the certificate expired", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.LoggedAt = time.Now().Add(time.Hour) })},
		{name: "no observer timestamp", wantErr: true, release: official(func(r *pluginsigtest.Release) { r.NoObserverTimestamp = true })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundleJSON, err := sigstore.Bundle(tc.release)
			if err != nil {
				t.Fatal(err)
			}
			result, err := pluginsig.NewVerifier(sigstore.TrustedMaterial).Verify(context.Background(), bundleJSON, digest, pluginsig.OfficialPolicy(tmdbRepo, tmdbVersion))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Verify() = %+v, want refused", result)
				}
				if errors.Is(err, pluginsig.ErrTrustRootUnavailable) {
					t.Fatalf("Verify() = %v, want a bundle failure, not a trust root one", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if result.SourceRepository != tmdbRepo || result.SourceRef != "refs/tags/v"+tmdbVersion {
				t.Fatalf("Verify() = %+v, want the tmdb tag", result)
			}
		})
	}

	// A signature over the binary itself carries no statement to name it as
	// provenance; sigstore-go accepts its digest, so the policy must refuse.
	t.Run("message signature without a statement", func(t *testing.T) {
		bundleJSON, err := sigstore.MessageSignatureBundle(official(nil), digest)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pluginsig.NewVerifier(sigstore.TrustedMaterial).Verify(context.Background(), bundleJSON, digest, pluginsig.OfficialPolicy(tmdbRepo, tmdbVersion))
		if err == nil || err.Error() != "bundle carries no in-toto statement" {
			t.Fatalf("Verify() error = %v, want the missing statement refused", err)
		}
	})

	t.Run("another Sigstore's bundle", func(t *testing.T) {
		other, err := pluginsigtest.New()
		if err != nil {
			t.Fatal(err)
		}
		bundleJSON, err := other.Bundle(official(nil))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pluginsig.NewVerifier(sigstore.TrustedMaterial).Verify(context.Background(), bundleJSON, digest, pluginsig.OfficialPolicy(tmdbRepo, tmdbVersion)); err == nil {
			t.Fatal("Verify() accepted a bundle the trust root did not issue")
		}
	})
}

// The advisory policy accepts any workflow in the catalog's repository, but
// still requires that repository and tag, and a policy without them is
// refused rather than checked loosely.
func TestVerifyAdvisoryPolicy(t *testing.T) {
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}
	const repo = "https://github.com/someone/silo-plugin-example"
	binary := []byte("plugin")
	digest := pluginsigtest.Digest(binary)
	release := pluginsigtest.Release{
		SAN:               repo + "/.github/workflows/release.yml@refs/tags/v0.4.0",
		Issuer:            pluginsig.GitHubActionsIssuer,
		SourceRepository:  repo,
		SourceRef:         "refs/tags/v0.4.0",
		RunnerEnvironment: "self-hosted",
		PredicateType:     pluginsig.SLSAProvenanceV1,
		Subjects:          map[string][]byte{"plugin-linux-amd64": digest},
	}
	bundleJSON, err := sigstore.Bundle(release)
	if err != nil {
		t.Fatal(err)
	}
	verifier := pluginsig.NewVerifier(sigstore.TrustedMaterial)
	if _, err := verifier.Verify(context.Background(), bundleJSON, digest, pluginsig.AdvisoryPolicy(repo, "0.4.0")); err != nil {
		t.Fatalf("advisory Verify() error = %v", err)
	}
	for name, policy := range map[string]pluginsig.Policy{
		"another repo":  pluginsig.AdvisoryPolicy("https://github.com/someone-else/silo-plugin-example", "0.4.0"),
		"no repo":       pluginsig.AdvisoryPolicy("", "0.4.0"),
		"no version":    pluginsig.AdvisoryPolicy(repo, ""),
		"official only": pluginsig.OfficialPolicy(repo, "0.4.0"),
	} {
		if _, err := verifier.Verify(context.Background(), bundleJSON, digest, policy); err == nil {
			t.Errorf("%s: Verify() accepted the bundle", name)
		}
	}
}

func TestVerifyReportsUnavailableTrustRoot(t *testing.T) {
	sigstore, err := pluginsigtest.New()
	if err != nil {
		t.Fatal(err)
	}
	digest := pluginsigtest.Digest([]byte("plugin"))
	bundleJSON, err := sigstore.Bundle(pluginsigtest.OfficialRelease(tmdbRepo, tmdbVersion, map[string][]byte{"plugin": digest}))
	if err != nil {
		t.Fatal(err)
	}
	unavailable := func(context.Context) (root.TrustedMaterial, error) { return nil, errors.New("offline") }
	_, err = pluginsig.NewVerifier(unavailable).Verify(context.Background(), bundleJSON, digest, pluginsig.OfficialPolicy(tmdbRepo, tmdbVersion))
	if !errors.Is(err, pluginsig.ErrTrustRootUnavailable) {
		t.Fatalf("Verify() error = %v, want ErrTrustRootUnavailable", err)
	}
}
