package localtls

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func expiringSource(t *testing.T) *certificateSource {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tls")
	if err := Ensure(dir, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	root, err := pair(filepath.Join(dir, "authority.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := Load(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	leaf := *cert.Leaf
	leaf.NotAfter = time.Now().Add(time.Hour)
	data, err := issue(&leaf, root.Leaf, root.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := write(filepath.Join(dir, "server.pem"), data); err != nil {
		t.Fatal(err)
	}
	cert, _, err = Load(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return &certificateSource{dir: dir, host: "127.0.0.1", cert: cert}
}

func TestRunningServerRenewsLeafWithoutChangingTrustedAuthority(t *testing.T) {
	source := expiringSource(t)
	before := source.cert.Leaf.SerialNumber.String()
	roots, fingerprint, _ := Authority(source.dir)
	h := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	h.Listener = tls.NewListener(h.Listener, &tls.Config{GetCertificate: source.get, MinVersion: tls.VersionTLS13})
	h.Start()
	defer h.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			res, err := client.Get("https://" + h.Listener.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			leaf := res.TLS.PeerCertificates[0]
			if leaf.SerialNumber.String() == before || time.Until(leaf.NotAfter) < 300*24*time.Hour {
				t.Error("old expiring leaf served")
			}
		})
	}
	wg.Wait()
	_, after, _ := Authority(source.dir)
	if fingerprint != after {
		t.Fatal("trusted CA changed during renewal")
	}
}

func TestRenewalFailureNeverServesExpiredCertificateOrReplacesCA(t *testing.T) {
	s := expiringSource(t)
	if err := os.Remove(filepath.Join(s.dir, "authority.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.get(nil); err != nil {
		t.Fatal("still-valid cached certificate unavailable", err)
	}
	expired := *s.cert.Leaf
	expired.NotAfter = time.Now().Add(-time.Second)
	s.cert.Leaf = &expired
	if _, err := s.get(nil); err == nil {
		t.Fatal("expired cached certificate served")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "authority.pem")); !os.IsNotExist(err) {
		t.Fatal("CA silently replaced")
	}
	if _, err := x509.ParseCertificate(s.cert.Certificate[0]); err != nil {
		t.Fatal(err)
	}
}
