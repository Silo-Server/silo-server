package s3client

import (
	"errors"
	"net/http"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

// maxRedirects mirrors net/http's default so a misconfigured endpoint that
// keeps answering 307 or 308 cannot spin an S3 call until its context expires.
const maxRedirects = 10

var errTooManyRedirects = errors.New("s3: stopped after 10 redirects")

// errInsecureRedirect stops a redirect from replaying a signed request, or a
// delivery URL's token, over plain HTTP after it started over HTTPS.
var errInsecureRedirect = errors.New("s3: refused redirect from https to a non-https URL")

// checkRedirectBase is the policy both shared clients keep: net/http's hop
// limit, which a custom CheckRedirect replaces, and no HTTPS downgrade.
func checkRedirectBase(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errInsecureRedirect
	}
	return nil
}

const (
	// s3MaxConnsPerHost bounds concurrent connections per endpoint for the whole
	// process. Matching the idle limit leaves room for a dial that loses the
	// race to a freed idle connection. The SDK default (2048 connections,
	// 10 idle) let short HEAD bursts complete TLS handshakes that never
	// carried a request, and providers such as Mega S4 block clients for that.
	s3MaxConnsPerHost = 16
	// s3MaxIdleConns leaves the process-wide idle pool unbounded (zero in
	// net/http). A global cap below hosts times the per-host cap would evict
	// idle connections behind the transport's back and reopen the unused
	// handshake problem once enough endpoints are configured. Each host is
	// bounded separately, and the SDK idle timeout reclaims unused connections.
	s3MaxIdleConns = 0
)

// All S3 clients share this pool. The SDK's dial, TLS and keep-alive defaults
// are kept, along with its restriction to method-preserving redirects.
var sharedTransportValue = awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
	tr.MaxConnsPerHost = s3MaxConnsPerHost
	tr.MaxIdleConnsPerHost = s3MaxConnsPerHost
	tr.MaxIdleConns = s3MaxIdleConns
}).GetTransport()

var sharedHTTPClientValue = &http.Client{
	Transport: sharedTransportValue,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if err := checkRedirectBase(req, via); err != nil {
			return err
		}
		if req.Response.StatusCode != http.StatusTemporaryRedirect && req.Response.StatusCode != http.StatusPermanentRedirect {
			return http.ErrUseLastResponse
		}
		if len(via) > 0 && via[len(via)-1].URL.Host != req.URL.Host {
			req.Header.Del("X-Amz-Security-Token")
		}
		return nil
	},
}

// Delivery URLs retain normal browser redirect behavior, apart from HTTPS
// downgrades, while sharing the storage connection pool. S3 uploads must never
// become GETs after a redirect.
var sharedDeliveryHTTPClient = &http.Client{Transport: sharedTransportValue, CheckRedirect: checkRedirectBase}

func sharedHTTPClient() *http.Client { return sharedHTTPClientValue }

func sharedTransport() *http.Transport { return sharedTransportValue }
