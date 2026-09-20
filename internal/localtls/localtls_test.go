package localtls

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedTLSAndAuthorityReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "https")
	if err := Ensure(dir, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	cert, roots, err := Load(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	s.StartTLS()
	defer s.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	if response, err := http.Get(s.URL); err == nil {
		response.Body.Close()
		t.Fatal("untrusted authority accepted")
	}
	if _, _, err = Load(dir, "192.168.1.99"); err == nil {
		t.Fatal("wrong server address accepted")
	}
	_, before, _ := Authority(dir)
	if err = Ensure(dir, "192.168.1.99"); err != nil {
		t.Fatal(err)
	}
	_, after, _ := Authority(dir)
	if before != after {
		t.Fatal("IP change rotated trusted authority")
	}
	if _, _, err = Load(dir, "192.168.1.99"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"authority.pem", "server.pem"} {
		info, _ := os.Stat(filepath.Join(dir, name))
		if info.Mode().Perm() != 0600 {
			t.Fatal("shared private key")
		}
	}
	if err = os.Remove(filepath.Join(dir, "authority.pem")); err != nil {
		t.Fatal(err)
	}
	if err = Ensure(dir, "192.168.1.99"); err == nil {
		t.Fatal("silently replaced missing authority")
	}
}

func TestRejectInvalidAndMismatchedAuthority(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "https")
	if err := Ensure(dir, "8.8.8.8"); err == nil {
		t.Fatal("public IP accepted")
	}
	if err := Ensure(dir, "192.168.1.2"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "https")
	if err := Ensure(other, "192.168.1.2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(other, PublicFile))
	if err := os.WriteFile(filepath.Join(dir, PublicFile), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dir, "192.168.1.2"); err == nil {
		t.Fatal("wrong certificate authority accepted")
	}
	if err := Ensure(dir, "192.168.1.2"); err == nil {
		t.Fatal("mismatched public authority silently replaced")
	}
}
