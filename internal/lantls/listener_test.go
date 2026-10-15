package lantls

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// serve runs an http.Server on a sniffing listener and returns its address.
func serve(t *testing.T, identity *Identity, sniffTimeout time.Duration, handler http.Handler) (string, *http.Server) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	listener := NewListener(inner, identity.Config(), sniffTimeout)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return inner.Addr().String(), srv
}

// pinnedClient trusts only a certificate whose key matches pin, the way the
// apps do. It offers h2 first, so a server that negotiated HTTP/2 over a
// connection http.Server cannot speak it on would show up as a failure.
func pinnedClient(pin string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			NextProtos:         []string{"h2", alpnHTTP1},
			InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate pins the key instead.
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				leaf, err := x509.ParseCertificate(raw[0])
				if err != nil {
					return err
				}
				if Pin(leaf) != pin {
					return errors.New("pin mismatch")
				}
				return nil
			},
		}},
	}
}

func schemeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			_, _ = fmt.Fprintf(w, "tls %s", r.TLS.NegotiatedProtocol)
			return
		}
		_, _ = io.WriteString(w, "plain")
	})
}

func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

func newIdentity(t *testing.T) *Identity {
	t.Helper()
	identity, err := New(testMasterKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return identity
}

func TestListenerServesPlainAndTLSOnOnePort(t *testing.T) {
	identity := newIdentity(t)
	addr, _ := serve(t, identity, 0, schemeHandler())

	if got := get(t, &http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/"); got != "plain" {
		t.Fatalf("plain request saw %q", got)
	}
	if got := get(t, pinnedClient(identity.Pin()), "https://"+addr+"/"); got != "tls "+alpnHTTP1 {
		t.Fatalf("TLS request saw %q", got)
	}
}

func TestListenerRejectsAnotherKey(t *testing.T) {
	addr, _ := serve(t, newIdentity(t), 0, schemeHandler())
	other, err := New([]byte(strings.Repeat("o", 48)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := pinnedClient(other.Pin()).Get("https://" + addr + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a client pinned to another deployment's key connected")
	}
}

func TestSilentConnectionDoesNotHoldUpOthers(t *testing.T) {
	identity := newIdentity(t)
	addr, _ := serve(t, identity, 300*time.Millisecond, schemeHandler())

	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = silent.Close() }()

	start := time.Now()
	if got := get(t, &http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/"); got != "plain" {
		t.Fatalf("plain request saw %q", got)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("a silent connection delayed the next one by %v", elapsed)
	}

	// Once the sniff window passes, the silent connection is plain HTTP and
	// its late request is still served.
	time.Sleep(400 * time.Millisecond)
	if _, err := io.WriteString(silent, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write late request: %v", err)
	}
	_ = silent.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(silent), nil)
	if err != nil {
		t.Fatalf("late request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "plain" {
		t.Fatalf("late request saw %q", body)
	}
}

func TestCloseDropsConnectionsStillBeingClassified(t *testing.T) {
	identity := newIdentity(t)
	addr, srv := serve(t, identity, time.Minute, schemeHandler())

	pending, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = pending.Close() }()
	// Let the listener accept it and start waiting for its first byte.
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = pending.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = pending.Read(make([]byte, 1))
	var ne net.Error
	switch {
	case err == nil:
		t.Fatal("a connection still being classified survived shutdown")
	case errors.As(err, &ne) && ne.Timeout():
		t.Fatal("a connection still being classified was left open after shutdown")
	}
}

func TestHijackWorksOnBothKinds(t *testing.T) {
	identity := newIdentity(t)
	addr, _ := serve(t, identity, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo " + line)
		_ = rw.Flush()
	}))

	upgrade := func(conn net.Conn) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("upgrade status = %d", resp.StatusCode)
		}
		_, _ = io.WriteString(conn, "hello\n")
		line, err := reader.ReadString('\n')
		if err != nil || line != "echo hello\n" {
			t.Fatalf("echo = %q, %v", line, err)
		}
	}

	plain, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = plain.Close() }()
	upgrade(plain)

	secure, err := tls.Dial("tcp", addr, pinnedClient(identity.Pin()).Transport.(*http.Transport).TLSClientConfig)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer func() { _ = secure.Close() }()
	upgrade(secure)
}

func TestReplayConnKeepsReaderFrom(t *testing.T) {
	var conn net.Conn = &replayConn{}
	if _, ok := conn.(io.ReaderFrom); !ok {
		t.Fatal("replayConn must implement io.ReaderFrom so responses keep the sendfile path")
	}
}
