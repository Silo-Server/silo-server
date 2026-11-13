// Package pluginsigtest mints Sigstore bundles shaped like the ones
// actions/attest-build-provenance publishes for a plugin release, against a
// throwaway trust root, so tests can drive the real sigstore-go verification
// path. Only tests import it.
//
// sigstore-go's testing/ca.VirtualSigstore issues e-mail leaf certificates
// without the GitHub Fulcio extensions, so this package keeps its Rekor log
// and timestamp authority and issues the leaf itself: a URI SAN, the issuer,
// runner environment and source repository extensions, and an embedded SCT
// from its own CT log.
package pluginsigtest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"time"

	ct "github.com/google/certificate-transparency-go"
	cttls "github.com/google/certificate-transparency-go/tls"
	ctx509 "github.com/google/certificate-transparency-go/x509"
	ctx509util "github.com/google/certificate-transparency-go/x509util"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
	sigdsse "github.com/sigstore/sigstore/pkg/signature/dsse"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Silo-Server/silo-server/internal/plugins/pluginsig"
)

const bundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// Sigstore is a throwaway Fulcio CA, CT log, Rekor log and timestamp
// authority.
type Sigstore struct {
	virtual            *ca.VirtualSigstore
	fulcioRoot         *x509.Certificate
	fulcioIntermediate *x509.Certificate
	fulcioKey          *ecdsa.PrivateKey
	ctKey              *ecdsa.PrivateKey
	ctLogID            [32]byte
}

// New returns a fresh Sigstore.
func New() (*Sigstore, error) {
	virtual, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, err
	}
	fulcioRoot, rootKey, err := ca.GenerateRootCa()
	if err != nil {
		return nil, err
	}
	intermediate, intermediateKey, err := ca.GenerateFulcioIntermediate(fulcioRoot, rootKey)
	if err != nil {
		return nil, err
	}
	ctKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ctDER, err := x509.MarshalPKIXPublicKey(ctKey.Public())
	if err != nil {
		return nil, err
	}
	return &Sigstore{
		virtual:            virtual,
		fulcioRoot:         fulcioRoot,
		fulcioIntermediate: intermediate,
		fulcioKey:          intermediateKey,
		ctKey:              ctKey,
		ctLogID:            sha256.Sum256(ctDER),
	}, nil
}

// TrustedMaterial is the trust root that verifies this Sigstore's bundles.
func (s *Sigstore) TrustedMaterial(context.Context) (root.TrustedMaterial, error) {
	return trustedMaterial{s}, nil
}

type trustedMaterial struct{ s *Sigstore }

func (m trustedMaterial) TimestampingAuthorities() []root.TimestampingAuthority {
	return m.s.virtual.TimestampingAuthorities()
}

func (m trustedMaterial) RekorLogs() map[string]*root.TransparencyLog {
	return m.s.virtual.RekorLogs()
}

func (m trustedMaterial) PublicKeyVerifier(keyID string) (root.TimeConstrainedVerifier, error) {
	return m.s.virtual.PublicKeyVerifier(keyID)
}

func (m trustedMaterial) FulcioCertificateAuthorities() []root.CertificateAuthority {
	return []root.CertificateAuthority{&root.FulcioCertificateAuthority{
		Root:                m.s.fulcioRoot,
		Intermediates:       []*x509.Certificate{m.s.fulcioIntermediate},
		ValidityPeriodStart: time.Now().Add(-time.Hour),
		ValidityPeriodEnd:   time.Now().Add(time.Hour),
		URI:                 "https://fulcio.test",
	}}
}

func (m trustedMaterial) CTLogs() map[string]*root.TransparencyLog {
	return map[string]*root.TransparencyLog{hex.EncodeToString(m.s.ctLogID[:]): {
		BaseURL:             "https://ctfe.test",
		ID:                  m.s.ctLogID[:],
		ValidityPeriodStart: time.Now().Add(-time.Hour),
		ValidityPeriodEnd:   time.Now().Add(time.Hour),
		HashFunc:            crypto.SHA256,
		PublicKey:           m.s.ctKey.Public(),
	}}
}

// Release describes the certificate and attestation of one signed release.
type Release struct {
	SAN               string
	Issuer            string
	SourceRepository  string
	SourceRef         string
	RunnerEnvironment string
	PredicateType     string
	// Subjects maps each attested file name to its SHA-256.
	Subjects map[string][]byte
	// LoggedAt is the integrated time Rekor records. Zero means now; a time
	// outside the certificate's ten-minute validity fails verification.
	LoggedAt time.Time
	// NoObserverTimestamp leaves out the log entry's signed timestamp, so the
	// bundle has only an inclusion proof and no observer timestamp.
	NoObserverTimestamp bool
}

// OfficialRelease is a release built by Silo's reusable workflow from the tag
// of version in repository.
func OfficialRelease(repository, version string, subjects map[string][]byte) Release {
	return Release{
		SAN:               pluginsig.OfficialSignerIdentity,
		Issuer:            pluginsig.GitHubActionsIssuer,
		SourceRepository:  repository,
		SourceRef:         pluginsig.TagRef(version),
		RunnerEnvironment: pluginsig.GitHubHostedRunner,
		PredicateType:     pluginsig.SLSAProvenanceV1,
		Subjects:          subjects,
	}
}

// Digest is the SHA-256 of data.
func Digest(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// Bundle returns the JSON Sigstore bundle for r.
func (s *Sigstore) Bundle(r Release) ([]byte, error) {
	leaf, leafKey, err := s.leafCertificate(r)
	if err != nil {
		return nil, err
	}
	statement, err := statementJSON(r)
	if err != nil {
		return nil, err
	}
	envelope, sig, err := signEnvelope(leaf, leafKey, statement)
	if err != nil {
		return nil, err
	}
	loggedAt := r.LoggedAt
	if loggedAt.IsZero() {
		loggedAt = time.Now()
	}
	entry, err := s.virtual.GenerateTlogEntry(leaf, envelope, sig, loggedAt.Unix(), true)
	if err != nil {
		return nil, err
	}
	tle, err := s.withPromise(entry.TransparencyLogEntry(), "dsse", r.NoObserverTimestamp)
	if err != nil {
		return nil, err
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, err
	}
	pb := &protobundle.Bundle{
		MediaType: bundleMediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:     &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw}},
			TlogEntries: []*protorekor.TransparencyLogEntry{tle},
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload:     payload,
			PayloadType: envelope.PayloadType,
			Signatures:  []*protodsse.Signature{{Sig: sig}},
		}},
	}
	return protojson.Marshal(pb)
}

// MessageSignatureBundle returns a bundle for r that signs digest directly,
// as cosign sign-blob does, with no in-toto statement. r.Subjects and
// r.PredicateType are unused.
func (s *Sigstore) MessageSignatureBundle(r Release, digest []byte) ([]byte, error) {
	leaf, leafKey, err := s.leafCertificate(r)
	if err != nil {
		return nil, err
	}
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, digest)
	if err != nil {
		return nil, err
	}
	certPEM, err := cryptoutils.MarshalCertificateToPEM(leaf)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.1",
		"kind":       "hashedrekord",
		"spec": map[string]any{
			"data": map[string]any{"hash": map[string]any{"algorithm": "sha256", "value": hex.EncodeToString(digest)}},
			"signature": map[string]any{
				"content":   base64.StdEncoding.EncodeToString(sig),
				"publicKey": map[string]any{"content": base64.StdEncoding.EncodeToString(certPEM)},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	logID, err := s.virtual.RekorLogID()
	if err != nil {
		return nil, err
	}
	logIDRaw, err := hex.DecodeString(logID)
	if err != nil {
		return nil, err
	}
	proof, err := s.virtual.GetInclusionProof(body)
	if err != nil {
		return nil, err
	}
	entry, err := tlog.NewEntry(body, time.Now().Unix(), 0, logIDRaw, nil, proof) //nolint:staticcheck // the constructor VirtualSigstore uses
	if err != nil {
		return nil, err
	}
	tle, err := s.withPromise(entry.TransparencyLogEntry(), "hashedrekord", r.NoObserverTimestamp)
	if err != nil {
		return nil, err
	}
	return protojson.Marshal(&protobundle.Bundle{
		MediaType: bundleMediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:     &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw}},
			TlogEntries: []*protorekor.TransparencyLogEntry{tle},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: digest},
			Signature:     sig,
		}},
	})
}

// withPromise completes a log entry with its kind and, unless noPromise,
// the signed entry timestamp that makes its integrated time an observer
// timestamp.
func (s *Sigstore) withPromise(tle *protorekor.TransparencyLogEntry, kind string, noPromise bool) (*protorekor.TransparencyLogEntry, error) {
	tle.KindVersion = &protorekor.KindVersion{Kind: kind, Version: "0.0.1"}
	if noPromise {
		return tle, nil
	}
	set, err := s.signedEntryTimestamp(tle)
	if err != nil {
		return nil, err
	}
	tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	return tle, nil
}

func (s *Sigstore) signedEntryTimestamp(tle *protorekor.TransparencyLogEntry) ([]byte, error) {
	return s.virtual.RekorSignPayload(tlog.RekorPayload{
		Body:           base64.StdEncoding.EncodeToString(tle.GetCanonicalizedBody()),
		IntegratedTime: tle.GetIntegratedTime(),
		LogIndex:       tle.GetLogIndex(),
		LogID:          hex.EncodeToString(tle.GetLogId().GetKeyId()),
	})
}

func statementJSON(r Release) ([]byte, error) {
	type subject struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}
	subjects := make([]subject, 0, len(r.Subjects))
	for name, digest := range r.Subjects {
		subjects = append(subjects, subject{Name: name, Digest: map[string]string{"sha256": hex.EncodeToString(digest)}})
	}
	return json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       subjects,
		"predicateType": r.PredicateType,
		"predicate":     map[string]any{"buildDefinition": map[string]any{"buildType": "https://actions.github.io/buildtypes/workflow/v1"}},
	})
}

func signEnvelope(leaf *x509.Certificate, key *ecdsa.PrivateKey, statement []byte) (*dsse.Envelope, []byte, error) {
	signer, err := signature.LoadECDSASignerVerifier(key, crypto.SHA256)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("leaf key is %T", leaf.PublicKey)
	}
	envelopeSigner, err := dsse.NewEnvelopeSigner(&sigdsse.SignerAdapter{SignatureSigner: signer, Pub: pub})
	if err != nil {
		return nil, nil, err
	}
	envelope, err := envelopeSigner.SignPayload(context.Background(), "application/vnd.in-toto+json", statement)
	if err != nil {
		return nil, nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(envelope.Signatures[0].Sig)
	if err != nil {
		return nil, nil, err
	}
	return envelope, sig, nil
}

// leafCertificate issues a Fulcio-shaped leaf for r with one embedded SCT. It
// first issues the certificate without the SCT list, which is the
// precertificate TBS a CT log signs, then issues it again with the list.
func (s *Sigstore) leafCertificate(r Release) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	san, err := url.Parse(r.SAN)
	if err != nil {
		return nil, nil, err
	}
	extensions, err := fulcioExtensions(r)
	if err != nil {
		return nil, nil, err
	}
	notBefore := time.Now().Add(-time.Minute)
	template := &x509.Certificate{
		SerialNumber:    big.NewInt(time.Now().UnixNano()),
		URIs:            []*url.URL{san},
		NotBefore:       notBefore,
		NotAfter:        notBefore.Add(10 * time.Minute),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: extensions,
	}
	precertDER, err := x509.CreateCertificate(rand.Reader, template, s.fulcioIntermediate, key.Public(), s.fulcioKey)
	if err != nil {
		return nil, nil, err
	}
	precert, err := x509.ParseCertificate(precertDER)
	if err != nil {
		return nil, nil, err
	}
	sctExtension, err := s.sctListExtension(precert.RawTBSCertificate)
	if err != nil {
		return nil, nil, err
	}
	template.ExtraExtensions = append(template.ExtraExtensions, sctExtension)
	leafDER, err := x509.CreateCertificate(rand.Reader, template, s.fulcioIntermediate, key.Public(), s.fulcioKey)
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, nil, err
	}
	return leaf, key, nil
}

func fulcioExtensions(r Release) ([]pkix.Extension, error) {
	values := []struct {
		oid   asn1.ObjectIdentifier
		value string
	}{
		{certificate.OIDIssuerV2, r.Issuer},
		{certificate.OIDBuildSignerURI, r.SAN},
		{certificate.OIDRunnerEnvironment, r.RunnerEnvironment},
		{certificate.OIDSourceRepositoryURI, r.SourceRepository},
		{certificate.OIDSourceRepositoryRef, r.SourceRef},
	}
	extensions := make([]pkix.Extension, 0, len(values))
	for _, v := range values {
		if v.value == "" {
			continue
		}
		der, err := asn1.MarshalWithParams(v.value, "utf8")
		if err != nil {
			return nil, err
		}
		extensions = append(extensions, pkix.Extension{Id: v.oid, Value: der})
	}
	return extensions, nil
}

func (s *Sigstore) sctListExtension(precertTBS []byte) (pkix.Extension, error) {
	sct := ct.SignedCertificateTimestamp{
		SCTVersion: ct.V1,
		LogID:      ct.LogID{KeyID: s.ctLogID},
		Timestamp:  uint64(time.Now().UnixMilli()),
	}
	leaf := ct.MerkleTreeLeaf{
		Version:  ct.V1,
		LeafType: ct.TimestampedEntryLeafType,
		TimestampedEntry: &ct.TimestampedEntry{
			Timestamp: sct.Timestamp,
			EntryType: ct.PrecertLogEntryType,
			PrecertEntry: &ct.PreCert{
				IssuerKeyHash:  sha256.Sum256(s.fulcioIntermediate.RawSubjectPublicKeyInfo),
				TBSCertificate: precertTBS,
			},
		},
	}
	input, err := ct.SerializeSCTSignatureInput(sct, ct.LogEntry{Leaf: leaf})
	if err != nil {
		return pkix.Extension{}, err
	}
	digest := sha256.Sum256(input)
	sig, err := ecdsa.SignASN1(rand.Reader, s.ctKey, digest[:])
	if err != nil {
		return pkix.Extension{}, err
	}
	sct.Signature = ct.DigitallySigned{
		Algorithm: cttls.SignatureAndHashAlgorithm{Hash: cttls.SHA256, Signature: cttls.ECDSA},
		Signature: sig,
	}
	list, err := ctx509util.MarshalSCTsIntoSCTList([]*ct.SignedCertificateTimestamp{&sct})
	if err != nil {
		return pkix.Extension{}, err
	}
	listBytes, err := cttls.Marshal(*list)
	if err != nil {
		return pkix.Extension{}, err
	}
	value, err := asn1.Marshal(listBytes)
	if err != nil {
		return pkix.Extension{}, err
	}
	return pkix.Extension{Id: asn1.ObjectIdentifier(ctx509.OIDExtensionCTSCT), Value: value}, nil
}
