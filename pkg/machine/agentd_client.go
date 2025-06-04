package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type AllowIPRequest struct {
	AllowIPs []string `json:"allowIPs"`
}

func AllowBuilderIPViaHTTP(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ip, err := extractIPFromEndpoint(endpoint)
	if err != nil {
		return fmt.Errorf("failed to extract IP from endpoint: %w", err)
	}

	req := AllowIPRequest{
		AllowIPs: []string{ip},
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:912/", bytes.NewBuffer(reqJSON))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		// The HTTP server only runs if the egress filter is enabled, so don't worry about not being able to connect
		return nil
	}
	defer resp.Body.Close()

	var response map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("failed to decode HTTP response: %w", err)
	}

	if success, ok := response["success"].(bool); ok && success {
		return nil
	}

	if errMsg, ok := response["error"].(string); ok {
		return fmt.Errorf("HTTP request failed: %s", errMsg)
	}

	return fmt.Errorf("unexpected HTTP response: %v", response)
}

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
