package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	ipc "github.com/james-barrow/golang-ipc"
)

type IPCAllowRequest struct {
	AllowIPs []string `json:"allowIPs"`
}

// AllowBuilderIPViaIPC sends the builder IP to the agentd IPC server to allow it through the firewall
func AllowBuilderIPViaIPC(ctx context.Context, endpoint string) error {
	// Wait at most 30 seconds to finish the request
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// first, just check if the ipc socket is actually running, since otherwise Dial will wait for a few seconds attempting to connect
	if _, err := os.Stat("/tmp/depot-agentd.sock"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
	}

	client, err := ipc.StartClient("depot-agentd", &ipc.ClientConfig{
		Encryption: false,
	})
	if err != nil {
		// If we can't connect to the IPC server, log it but don't fail the build
		// The IPC server only runs if the egress filter is enabled
		return nil
	}
	defer client.Close()

	// Extract the IP from the endpoint (format is usually "tcp://ip:port")
	ip, err := extractIPFromEndpoint(endpoint)
	if err != nil {
		return fmt.Errorf("failed to extract IP from endpoint: %w", err)
	}

connected:
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for connection")
		default:
			if client.Status() == "Connected" {
				break connected
			}
			time.Sleep(10 * time.Millisecond)
		}

	}

	req := IPCAllowRequest{
		AllowIPs: []string{ip},
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal IPC request: %w", err)
	}

	if err := client.Write(1, reqJSON); err != nil {
		return fmt.Errorf("failed to send IPC request: %w", err)
	}

	// Wait for response
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for response to allow buildkit IP")
		default:
			msg, err := client.Read()
			if err != nil {
				continue
			}

			if msg.MsgType > 0 {
				var response map[string]any
				if err := json.Unmarshal(msg.Data, &response); err != nil {
					return fmt.Errorf("failed to unmarshal IPC response: %w", err)
				}

				if success, ok := response["success"].(bool); ok && success {
					return nil
				}

				if errMsg, ok := response["error"].(string); ok {
					return fmt.Errorf("IPC request failed: %s", errMsg)
				}

				return fmt.Errorf("unexpected IPC response: %v", response)
			}
		}
	}
}

// extractIPFromEndpoint extracts the IP address from an endpoint string
func extractIPFromEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimPrefix(endpoint, "tcp://")

	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to split host and port: %w", err)
	}

	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("not a valid IP address: %s", host)
	}

	return host, nil
}
