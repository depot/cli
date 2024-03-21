package connection

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/depot/cli/pkg/machine"
)

// Connects to the buildkitd using the TLS certs provided by the Depot API.
// Attempts to connect every one second for two minutes until it succeeds
// or the context is canceled.
func TLSConn(ctx context.Context, builder *machine.Machine) (net.Conn, error) {
	// Uses similar retry logic as the depot buildx driver.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM([]byte(builder.CACert)); !ok {
		return nil, fmt.Errorf("failed to append ca certs")
	}

	cfg := &tls.Config{RootCAs: certPool}
	if builder.Cert != "" || builder.Key != "" {
		cert, err := tls.X509KeyPair([]byte(builder.Cert), []byte(builder.Key))
		if err != nil {
			return nil, fmt.Errorf("could not read certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	dialer := &tls.Dialer{Config: cfg}
	addr := strings.TrimPrefix(builder.Addr, "tcp://")

	var (
		conn net.Conn
		err  error
	)
	for i := 0; i < 120; i++ {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			time.Sleep(1 * time.Second)
		}
	}

	return nil, err
}
