package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLANAddressChangesAreNotReportedAsPortConflicts(t *testing.T) {
	available := []string{"192.168.50.228", "fd00::1"}
	for _, address := range []string{"", "192.168.50.228", "fd00::1"} {
		if err := checkLANAddress(address, available); err != nil {
			t.Fatal(err)
		}
	}
	for _, addresses := range [][]string{available, {}} {
		err := checkLANAddress("192.168.50.33", addresses)
		var field *fieldError
		if !errors.As(err, &field) || field.Field != "lanAddress" || !strings.Contains(err.Error(), "192.168.50.33") || strings.Contains(err.Error(), "端口") {
			t.Fatalf("incorrect missing-IP diagnosis: %v", err)
		}
		if len(addresses) > 0 && !strings.Contains(err.Error(), "192.168.50.228") {
			t.Fatal("current address missing from guidance")
		}
	}
}

func TestManagerMissingAddressPreservesCredentialConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	c := localConfig{Data: filepath.Join(root, "library"), Port: 19473, LANAddress: "192.168.7.10"}
	available, err := localLANAddresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range available {
		if address == c.LANAddress {
			t.Skip("synthetic unavailable IP belongs to this test machine")
		}
	}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword, VaultPassword: "synthetic-network-vault-password", VaultIdleMinutes: 7}); err != nil {
		t.Fatal(err)
	}
	status, err := readManagerStatus(context.Background(), path)
	if err != nil || status.NetworkError == "" || !status.PasswordSet || !status.VaultSet || status.VaultIdleMinutes != 7 || status.Config.LANAddress != c.LANAddress {
		t.Fatalf("IP loss hid credentials or silently changed the configured address: %+v, %v", status, err)
	}
	if runtime.GOOS == "darwin" {
		err = startManaged(context.Background(), path)
		var field *fieldError
		if !errors.As(err, &field) || field.Field != "lanAddress" {
			t.Fatalf("start did not identify the invalid address: %v", err)
		}
	}
}

func TestManagedServiceProbeRejectsUnhealthyResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"http-error", http.StatusServiceUnavailable, `{}`},
		{"invalid-json", http.StatusOK, `<html>error</html>`},
		{"unconfigured", http.StatusOK, `{"configured":false}`},
		{"missing-status", http.StatusOK, `{}`},
		{"trailing-data", http.StatusOK, `{"configured":true}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if _, err := probeManagedService(context.Background(), server.URL, server.Client().Transport); err == nil {
				t.Fatal("unhealthy process reported a usable library")
			}
		})
	}
	t.Run("redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("status probe followed a redirect")
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
		defer server.Close()
		if _, err := probeManagedService(context.Background(), server.URL, server.Client().Transport); err == nil {
			t.Fatal("redirect reported a healthy service")
		}
	})
	t.Run("healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/auth" {
				t.Error("unexpected probe path")
			}
			w.Write([]byte(`{"configured":true,"vaultConfigured":true,"vaultIdleMinutes":7}`))
		}))
		defer server.Close()
		auth, err := probeManagedService(context.Background(), server.URL, server.Client().Transport)
		if err != nil || !auth.Configured || !auth.VaultConfigured || auth.VaultIdleMinutes != 7 {
			t.Fatal("healthy library did not report its existing settings", auth, err)
		}
	})
}

func TestManagedServiceProbeDeadlineIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"configured":true`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	started := time.Now()
	if _, err := probeManagedService(context.Background(), server.URL, server.Client().Transport); err == nil {
		t.Fatal("stalled response body was accepted")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("service status check did not return promptly")
	}
}

func TestListenErrorsPreserveTheirActualCause(t *testing.T) {
	for _, tc := range []struct {
		err   error
		field string
		text  string
	}{
		{syscall.EADDRNOTAVAIL, "lanAddress", "该 IP 已不在本机"},
		{syscall.EADDRINUSE, "port", "端口已被占用"},
	} {
		wrapped := &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: tc.err}}
		err := listenError("192.168.50.33:19473", wrapped)
		var field *fieldError
		if !errors.As(err, &field) || field.Field != tc.field || !strings.Contains(err.Error(), tc.text) {
			t.Fatalf("incorrect diagnosis for %v: %v", tc.err, err)
		}
	}
	if err := listenError("127.0.0.1:19473", syscall.EACCES); !errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "占用") {
		t.Fatal("unrelated bind error misreported", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	other, err := net.Listen("tcp", listener.Addr().String())
	if err == nil {
		other.Close()
		t.Fatal("occupied-port fixture was not occupied")
	}
	var field *fieldError
	if !errors.As(listenError(listener.Addr().String(), err), &field) || field.Field != "port" {
		t.Fatal("real occupied port was not diagnosed correctly", err)
	}
}
