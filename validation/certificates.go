package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// buildkitServerName is the TLS server name of the buildkitd daemons. The
// Depot API returns it with the builder connection.
const buildkitServerName = "buildkitd"

// certificates holds a certificate authority, a buildkitd server
// certificate, and a client certificate, all in PEM form. Depot builders
// require mutual TLS, so the harness does too.
type certificates struct {
	CA           string
	ServerCert   string
	ServerKey    string
	ClientCert   string
	ClientKey    string
	RegistryCert string
	RegistryKey  string
}

func newCertificates(serverIP net.IP) (*certificates, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "depot-validation-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	issue := func(serial int64, usage x509.ExtKeyUsage, dnsNames []string, ips []net.IP) (string, string, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", "", err
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: dnsNames[0]},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			DNSNames:     dnsNames,
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			return "", "", err
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return "", "", err
		}
		return encodePEM("CERTIFICATE", der), encodePEM("EC PRIVATE KEY", keyDER), nil
	}

	serverCert, serverKey, err := issue(2, x509.ExtKeyUsageServerAuth, []string{buildkitServerName}, []net.IP{serverIP})
	if err != nil {
		return nil, err
	}
	clientCert, clientKey, err := issue(3, x509.ExtKeyUsageClientAuth, []string{"depot-validation-client"}, nil)
	if err != nil {
		return nil, err
	}
	registryCert, registryKey, err := issue(4, x509.ExtKeyUsageServerAuth, []string{registryHost, "localhost"}, []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback})
	if err != nil {
		return nil, err
	}
	return &certificates{
		CA:           encodePEM("CERTIFICATE", caDER),
		ServerCert:   serverCert,
		ServerKey:    serverKey,
		ClientCert:   clientCert,
		ClientKey:    clientKey,
		RegistryCert: registryCert,
		RegistryKey:  registryKey,
	}, nil
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

// writeFiles writes the certificates for buildkitd, for the registry, and
// for clients.
func (c *certificates) writeFiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"ca.pem":           c.CA,
		"cert.pem":         c.ServerCert,
		"key.pem":          c.ServerKey,
		"client.pem":       c.ClientCert,
		"client-key.pem":   c.ClientKey,
		"registry.pem":     c.RegistryCert,
		"registry-key.pem": c.RegistryKey,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pool returns a certificate pool that trusts the certificate authority.
func (c *certificates) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(c.CA))
	return pool
}
