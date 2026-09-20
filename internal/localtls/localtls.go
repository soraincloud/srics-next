// Package localtls manages one installation's private LAN certificate authority.
// Only the public .cer file is exported; keys stay outside the library/backup.
package localtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
)

const PublicFile = "SRICS-Local-CA.cer"

func pair(path string) (tls.Certificate, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, errors.New("HTTPS 密钥必须为仅本人可读的普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer clear(data)
	cert, err := tls.X509KeyPair(data, data)
	if err == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	}
	return cert, err
}

func write(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tls-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func issue(template, parent *x509.Certificate, signer *ecdsa.PrivateKey) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template.SerialNumber, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	defer clear(private)
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})...), nil
}

// Ensure creates a CA once and renews the server certificate on save/start.
// An incomplete or damaged existing authority is an error, never a silent rotation.
func Ensure(dir, host string) error {
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errors.New("HTTPS 仅支持局域网 IP")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("HTTPS 目录必须仅本人可访问")
	}
	rootPath := filepath.Join(dir, "authority.pem")
	root, err := pair(rootPath)
	if os.IsNotExist(err) {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return errors.New("HTTPS 根密钥缺失，未替换已有证书")
		}
		now := time.Now()
		ca := &x509.Certificate{Subject: pkix.Name{CommonName: "SRICS Next Local CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{"localhost"}}
		for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "fc00::/7", "::1/128"} {
			_, network, _ := net.ParseCIDR(cidr)
			ca.PermittedIPRanges = append(ca.PermittedIPRanges, network)
		}
		data, e := issue(ca, nil, nil)
		if e != nil {
			return e
		}
		defer clear(data)
		if e = atomicfile.WriteNew(rootPath, func(w io.Writer) error { _, e := w.Write(data); return e }); e != nil && !os.IsExist(e) {
			return e
		}
		root, err = pair(rootPath)
	}
	if err != nil {
		return err
	}
	ca := root.Leaf
	if !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || time.Now().Before(ca.NotBefore) || time.Now().AddDate(1, 0, 0).After(ca.NotAfter) {
		return errors.New("HTTPS 根证书无效或即将到期，请重新配置证书信任")
	}
	signer, ok := root.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("HTTPS 根密钥格式不正确")
	}
	publicPath := filepath.Join(dir, PublicFile)
	public, err := os.ReadFile(publicPath)
	if os.IsNotExist(err) {
		if err = atomicfile.WriteNew(publicPath, func(w io.Writer) error { _, e := w.Write(ca.Raw); return e }); err != nil && !os.IsExist(err) {
			return err
		}
	} else if err != nil {
		return err
	} else if string(public) != string(ca.Raw) {
		return errors.New("HTTPS 公共证书与根密钥不匹配")
	}
	if cert, _, err := Load(dir, host); err == nil && time.Now().AddDate(0, 0, 30).Before(cert.Leaf.NotAfter) {
		return nil
	}
	now := time.Now()
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "SRICS Next"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), IPAddresses: []net.IP{ip}, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	data, err := issue(leaf, ca, signer)
	if err != nil {
		return err
	}
	defer clear(data)
	return write(filepath.Join(dir, "server.pem"), data)
}

func Authority(dir string) (*x509.CertPool, string, error) {
	der, err := os.ReadFile(filepath.Join(dir, PublicFile))
	if err != nil {
		return nil, "", err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil || !ca.IsCA {
		return nil, "", errors.New("HTTPS 公共证书无效")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, fmt.Sprintf("%X", sha256.Sum256(der)), nil
}

func Load(dir, host string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := pair(filepath.Join(dir, "server.pem"))
	if err != nil {
		return cert, nil, err
	}
	roots, _, err := Authority(dir)
	if err != nil {
		return cert, nil, err
	}
	_, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
	return cert, roots, err
}
