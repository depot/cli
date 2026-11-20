//go:build !windows

package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/cli/cli/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/progress/progresswriter"
	"github.com/stretchr/testify/require"
)

func TestCredentialHelperTimeout(t *testing.T) {
	binDir := t.TempDir()
	helpers := map[string]string{
		// stderr is closed so the abandoned helper does not hold the test's output open.
		"hang": "exec 2>/dev/null\nsleep 10\n",
		"slow": "sleep 1.5\necho '{\"Username\":\"slow-user\",\"Secret\":\"slow-secret\"}'\n",
		// Hangs past the limit on its first call only, then answers.
		"flaky": "if [ ! -e \"$0.called\" ]; then touch \"$0.called\"; exec 2>/dev/null; sleep 3; fi\necho '{\"Username\":\"flaky-user\",\"Secret\":\"flaky-secret\"}'\n",
		// Fails on its first call only.
		"broken": "if [ ! -e \"$0.called\" ]; then touch \"$0.called\"; exit 1; fi\necho '{\"Username\":\"broken-user\",\"Secret\":\"broken-secret\"}'\n",
		"fast":   "echo '{\"Username\":\"fast-user\",\"Secret\":\"fast-secret\"}'\n",
	}
	for name, body := range helpers {
		path := filepath.Join(binDir, "docker-credential-"+name)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := t.TempDir()
	dockerConfig := `{"credsStore":"hang","credHelpers":{"slow.example.com":"slow","fast.example.com":"fast","flaky-a.example.com":"flaky","flaky-b.example.com":"flaky","broken.example.com":"broken"}}`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(dockerConfig), 0o600))
	previousDir := config.Dir()
	config.SetDir(configDir)
	t.Cleanup(func() { config.SetDir(previousDir) })

	t.Setenv("DEPOT_PUSH_REGISTRY_USERNAME", "")
	t.Setenv("DEPOT_PUSH_REGISTRY_PASSWORD", "")
	t.Setenv("DEPOT_PUSH_REGISTRY_AUTH", "")

	timeout := 2 * time.Second
	previousTimeout := credentialHelperTimeout
	credentialHelperTimeout = timeout
	t.Cleanup(func() { credentialHelperTimeout = previousTimeout })

	provider := NewDockerAuthProviderWithDepotAuth()
	var logMu sync.Mutex
	logLines := map[string]string{}
	withLogger, ok := provider.(interface{ SetLogger(progresswriter.Logger) })
	if ok {
		withLogger.SetLogger(func(s *client.SolveStatus) {
			logMu.Lock()
			defer logMu.Unlock()
			for _, v := range s.Vertexes {
				logLines[v.Digest.String()] = v.Name
			}
		})
	}
	server := provider.(auth.AuthServer)

	type result struct {
		resp *auth.CredentialsResponse
		err  error
	}
	call := func(host string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			resp, err := server.Credentials(context.Background(), &auth.CredentialsRequest{Host: host})
			ch <- result{resp: resp, err: err}
		}()
		return ch
	}

	hung := call("registry-1.docker.io")
	time.Sleep(100 * time.Millisecond)

	select {
	case r := <-call("fast.example.com"):
		require.NoError(t, r.err)
		require.Equal(t, "fast-secret", r.resp.Secret)
	case <-time.After(timeout / 2):
		t.Fatal("a host with a working helper waited behind the hung helper")
	}

	select {
	case r := <-hung:
		require.NoError(t, r.err)
		require.Empty(t, r.resp.Secret)
	case <-time.After(timeout + 2*time.Second):
		t.Fatal("the hung helper lookup never returned")
	}

	select {
	case r := <-call("registry-1.docker.io"):
		require.NoError(t, r.err)
		require.Empty(t, r.resp.Secret)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a host whose helper already timed out waited again")
	}

	r := <-call("slow.example.com")
	require.NoError(t, r.err)
	require.Equal(t, "slow-secret", r.resp.Secret, "a helper that answers before the limit must still be used")

	r = <-call("broken.example.com")
	require.Error(t, r.err)
	r = <-call("broken.example.com")
	require.NoError(t, r.err)
	require.Equal(t, "broken-secret", r.resp.Secret, "a helper error must not be cached")

	flakyStart := time.Now()
	r = <-call("flaky-a.example.com")
	require.NoError(t, r.err)
	require.Empty(t, r.resp.Secret)
	r = <-call("flaky-b.example.com")
	require.NoError(t, r.err)
	require.Empty(t, r.resp.Secret, "a host whose helper is hung is not asked again right away")
	time.Sleep(time.Until(flakyStart.Add(4 * time.Second)))
	r = <-call("flaky-b.example.com")
	require.NoError(t, r.err)
	require.Equal(t, "flaky-secret", r.resp.Secret, "a helper that answers again must be used again")

	require.True(t, ok, "auth provider must accept the build progress logger")
	logMu.Lock()
	defer logMu.Unlock()
	var timeoutLines []string
	for _, line := range logLines {
		if strings.Contains(line, "docker-credential-hang did not respond") {
			timeoutLines = append(timeoutLines, line)
		}
	}
	require.Len(t, timeoutLines, 1)
	require.Contains(t, timeoutLines[0], "docker-credential-hang")
	require.Contains(t, timeoutLines[0], "registry-1.docker.io")
	require.Contains(t, timeoutLines[0], "continuing without credentials")
}
