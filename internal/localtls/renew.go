package localtls

import (
	"crypto/tls"
	"errors"
	"log"
	"sync"
	"time"
)

type certificateSource struct {
	mu        sync.Mutex
	dir, host string
	cert      tls.Certificate
	nextCheck time.Time
}

// ServerConfig renews the leaf certificate during normal TLS handshakes,
// retaining the installation's trusted CA. Long-running servers need no restart.
func ServerConfig(dir, host string) (*tls.Config, error) {
	if err := Ensure(dir, host); err != nil {
		return nil, err
	}
	cert, _, err := Load(dir, host)
	if err != nil {
		return nil, err
	}
	s := &certificateSource{dir: dir, host: host, cert: cert, nextCheck: time.Now().Add(24 * time.Hour)}
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: s.get}, nil
}

func (s *certificateSource) get(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !now.Before(s.nextCheck) || !now.Before(s.cert.Leaf.NotAfter) {
		err := Ensure(s.dir, s.host)
		if err == nil {
			var cert tls.Certificate
			cert, _, err = Load(s.dir, s.host)
			if err == nil {
				s.cert = cert
			}
		}
		s.nextCheck = now.Add(24 * time.Hour)
		if err != nil {
			s.nextCheck = now.Add(time.Hour)
			log.Print("SRICS: 局域网 HTTPS 证书续期失败，请检查 HTTPS 目录和根证书有效期")
			if !now.Before(s.cert.Leaf.NotAfter) {
				return nil, err
			}
		}
	}
	if now.Before(s.cert.Leaf.NotBefore) || !now.Before(s.cert.Leaf.NotAfter) {
		return nil, errors.New("HTTPS 证书已失效，请检查系统时间和证书配置")
	}
	cert := s.cert
	return &cert, nil
}
