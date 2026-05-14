package ci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func restoreArtifactAPIs(t *testing.T) {
	t.Helper()

	originalList := ciListArtifacts
	originalDownloadURL := ciGetArtifactDownloadURL
	originalClient := ciArtifactDownloadClient
	originalProgressInteractive := ciArtifactDownloadProgressInteractive

	t.Cleanup(func() {
		ciListArtifacts = originalList
		ciGetArtifactDownloadURL = originalDownloadURL
		ciArtifactDownloadClient = originalClient
		ciArtifactDownloadProgressInteractive = originalProgressInteractive
	})
}

func TestArtifactsCommandSurface(t *testing.T) {
	cmd := NewCmdArtifacts()
	subcommands := map[string]bool{}
	for _, subcommand := range cmd.Commands() {
		subcommands[subcommand.Name()] = true
	}
	if len(subcommands) != 2 || !subcommands["list"] || !subcommands["download"] {
		t.Fatalf("subcommands = %v, want list and download only", subcommands)
	}
	if subcommands["all"] || subcommands["download-all"] {
		t.Fatal("artifacts command should not expose download-all")
	}

	download := NewCmdArtifactsDownload()
	if download.Flags().Lookup("force") != nil {
		t.Fatal("download command should not expose --force")
	}
	if download.Flags().Lookup("output") != nil {
		t.Fatal("download command should use --output-file, not --output")
	}
	if download.Flags().Lookup("output-file") == nil {
		t.Fatal("download command should expose --output-file")
	}
}

func TestArtifactsListPrintsTableAndPassesFilters(t *testing.T) {
	restoreArtifactAPIs(t)

	var capturedToken string
	var capturedOrgID string
	var capturedRunID string
	var capturedOptions api.CIListArtifactsOptions
	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedRunID = runID
		capturedOptions = options
		return []*civ1.Artifact{
			testArtifact("artifact-123456789012345678901234567890", "coverage.txt"),
		}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{
		"--org", "org-123",
		"--token", "token-123",
		"--workflow", "workflow-123",
		"--job", "job-123",
		"--attempt", "attempt-123",
		"run-123",
	})
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if capturedToken != "token-123" {
		t.Fatalf("token = %q, want token-123", capturedToken)
	}
	if capturedOrgID != "org-123" {
		t.Fatalf("orgID = %q, want org-123", capturedOrgID)
	}
	if capturedRunID != "run-123" {
		t.Fatalf("runID = %q, want run-123", capturedRunID)
	}
	if capturedOptions.WorkflowID != "workflow-123" || capturedOptions.JobID != "job-123" || capturedOptions.AttemptID != "attempt-123" {
		t.Fatalf("options = %+v, want workflow/job/attempt filters", capturedOptions)
	}

	output := stdout.String()
	for _, want := range []string{
		"ARTIFACT ID",
		"artifact-123456789012345678901234567890",
		"coverage.txt",
		"1.5 KB",
		".depot/build.yml",
		"build",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("table output missing %q:\n%s", want, output)
		}
	}
}

func TestArtifactsListAlignsLongArtifactIDColumn(t *testing.T) {
	restoreArtifactAPIs(t)

	id := "019e2677-f3d6-7d66-9997-f31f7110a0a9"
	name := "regression-test-artifact"
	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		return []*civ1.Artifact{testArtifact(id, name)}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "run-123"})
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("table output lines = %d, want 2:\n%s", len(lines), stdout.String())
	}
	headerNameColumn := strings.Index(lines[0], "NAME")
	rowNameColumn := strings.Index(lines[1], name)
	if headerNameColumn == -1 || rowNameColumn == -1 {
		t.Fatalf("table output missing name column:\n%s", stdout.String())
	}
	if rowNameColumn != headerNameColumn {
		t.Fatalf("artifact name column = %d, want header name column %d:\n%s", rowNameColumn, headerNameColumn, stdout.String())
	}
}

func TestArtifactsListAcceptsExplicitTextOutputMode(t *testing.T) {
	restoreArtifactAPIs(t)

	called := false
	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		called = true
		return []*civ1.Artifact{testArtifact("artifact-1", "coverage.txt")}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "text", "run-123"})
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if !called {
		t.Fatal("expected list API to be called")
	}
	if !strings.Contains(stdout.String(), "coverage.txt") {
		t.Fatalf("table output missing artifact: %s", stdout.String())
	}
}

func TestArtifactsListRejectsUnknownOutputMode(t *testing.T) {
	restoreArtifactAPIs(t)

	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		return nil, errors.New("should not be called")
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "yaml", "run-123"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected unsupported output error")
	}
	if !strings.Contains(err.Error(), `unsupported output "yaml" (valid: text, json)`) {
		t.Fatalf("error = %v", err)
	}
}

func TestArtifactsListSanitizesTextTableControlCharacters(t *testing.T) {
	restoreArtifactAPIs(t)

	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		return []*civ1.Artifact{
			{
				ArtifactId:   "artifact-1",
				RunId:        "run-123",
				WorkflowId:   "workflow-123",
				WorkflowPath: ".depot/\x1b]52;c;secret\a.yml",
				JobId:        "job-123",
				JobKey:       "build\x1b[31m",
				AttemptId:    "attempt-123",
				Attempt:      1,
				Name:         "coverage\x1b]52;c;secret\a.txt",
				SizeBytes:    1536,
				CreatedAt:    "2026-05-11T12:00:00Z",
			},
		}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "run-123"})
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	output := stdout.String()
	if strings.ContainsRune(output, '\x1b') || strings.ContainsRune(output, '\a') {
		t.Fatalf("table output contains terminal control character: %q", output)
	}
	if !strings.Contains(output, "coverage_]52;c;secret_.txt") {
		t.Fatalf("table output did not include sanitized artifact name: %q", output)
	}
}

func TestArtifactsListTruncatesTextTableByRunes(t *testing.T) {
	restoreArtifactAPIs(t)

	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		return []*civ1.Artifact{
			testArtifact("artifact-1", "測試測試測試測試測試測試測試測試測試測試.txt"),
		}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "run-123"})
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if !utf8.ValidString(stdout.String()) {
		t.Fatalf("table output is not valid UTF-8: %q", stdout.String())
	}
}

func TestArtifactsListJSONOmitsDownloadURL(t *testing.T) {
	restoreArtifactAPIs(t)

	ciListArtifacts = func(ctx context.Context, token, orgID, runID string, options api.CIListArtifactsOptions) ([]*civ1.Artifact, error) {
		return []*civ1.Artifact{
			testArtifact("artifact-1", "coverage.txt"),
		}, nil
	}

	cmd := NewCmdArtifactsList()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "run-123"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string][]map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if len(raw["artifacts"]) != 1 {
		t.Fatalf("len(raw artifacts) = %d, want 1", len(raw["artifacts"]))
	}
	for _, key := range []string{"url", "download_url"} {
		if _, ok := raw["artifacts"][0][key]; ok {
			t.Fatalf("list JSON should not include %q: %s", key, stdout)
		}
	}

	var doc artifactsListJSON
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if len(doc.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(doc.Artifacts))
	}
	artifact := doc.Artifacts[0]
	if artifact.ArtifactID != "artifact-1" || artifact.Name != "coverage.txt" || artifact.SizeBytes != 1536 {
		t.Fatalf("unexpected JSON artifact: %+v", artifact)
	}
}

func TestArtifactsDownloadStreamsToOutputPath(t *testing.T) {
	restoreArtifactAPIs(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/artifact" {
			t.Fatalf("path = %s, want /artifact", r.URL.Path)
		}
		_, _ = io.WriteString(w, "artifact bytes")
	}))
	t.Cleanup(server.Close)

	var capturedToken string
	var capturedOrgID string
	var capturedArtifactID string
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		capturedToken = token
		capturedOrgID = orgID
		capturedArtifactID = artifactID
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt", SizeBytes: 14},
			Url:      server.URL + "/artifact",
		}, nil
	}

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if capturedToken != "token-123" || capturedOrgID != "org-123" || capturedArtifactID != "artifact-1" {
		t.Fatalf("captured token/org/artifact = %q/%q/%q", capturedToken, capturedOrgID, capturedArtifactID)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "artifact bytes" {
		t.Fatalf("downloaded content = %q", content)
	}
}

func TestArtifactsDownloadReportsProgressForKnownSize(t *testing.T) {
	restoreArtifactAPIs(t)

	ciArtifactDownloadProgressInteractive = func() bool { return true }

	body := strings.Repeat("x", 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt", SizeBytes: int64(len(body))},
			Url:      server.URL,
		}, nil
	}

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	var stderr strings.Builder
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	output := stderr.String()
	for _, want := range []string{"Downloading", "1.0 KB / 1.0 KB", "100%", "Downloaded artifact-1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestArtifactDownloadProgressReportsUnknownSize(t *testing.T) {
	restoreArtifactAPIs(t)

	ciArtifactDownloadProgressInteractive = func() bool { return true }

	var stderr strings.Builder
	progress := newArtifactDownloadProgress(&stderr, -1)
	progress.Start()
	if _, err := progress.Write([]byte(strings.Repeat("x", 1536))); err != nil {
		t.Fatal(err)
	}
	progress.Finish()

	output := stderr.String()
	if !strings.Contains(output, "Downloading 1.5 KB") {
		t.Fatalf("progress output missing downloaded size:\n%s", output)
	}
	if strings.Contains(output, " / ") {
		t.Fatalf("unknown-size progress should not show total:\n%s", output)
	}
}

func TestArtifactsDownloadRefusesExistingOutput(t *testing.T) {
	restoreArtifactAPIs(t)

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	if err := os.WriteFile(destination, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	serverHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHit = true
		_, _ = io.WriteString(w, "new")
	}))
	t.Cleanup(server.Close)

	rpcCalled := false
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		rpcCalled = true
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt"},
			Url:      server.URL,
		}, nil
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected existing output error")
	}
	if strings.Contains(err.Error(), "force") {
		t.Fatalf("error = %v, should not mention force", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing file error", err)
	}
	if rpcCalled {
		t.Fatal("download URL RPC should not be called when explicit output exists")
	}
	if serverHit {
		t.Fatal("download server should not be hit when output exists")
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("file content = %q, want existing", content)
	}
}

func TestArtifactsDownloadRefusesExistingDefaultOutputBeforeHTTP(t *testing.T) {
	restoreArtifactAPIs(t)

	dir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	if err := os.WriteFile("coverage.txt", []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	serverHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHit = true
		_, _ = io.WriteString(w, "new")
	}))
	t.Cleanup(server.Close)

	rpcCalled := false
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		rpcCalled = true
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt"},
			Url:      server.URL,
		}, nil
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected existing default output error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing file error", err)
	}
	if !rpcCalled {
		t.Fatal("download URL RPC should be called to resolve default filename")
	}
	if serverHit {
		t.Fatal("download server should not be hit when default output exists")
	}
	content, err := os.ReadFile("coverage.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("file content = %q, want existing", content)
	}
}

func TestArtifactsDownloadValidatesResponseAndOutput(t *testing.T) {
	restoreArtifactAPIs(t)

	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return nil, errors.New("should not be called")
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", "-", "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected output validation error")
	}
	if !strings.Contains(err.Error(), "--output-file - is not supported") {
		t.Fatalf("error = %v", err)
	}

	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return &civ1.GetArtifactDownloadURLResponse{Url: "https://example.test/artifact"}, nil
	}
	cmd = NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected missing artifact metadata error")
	}
	if !strings.Contains(err.Error(), "artifact metadata") {
		t.Fatalf("error = %v", err)
	}
}

func TestArtifactsDownloadCleansUpFailedHTTPDownloads(t *testing.T) {
	restoreArtifactAPIs(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt"},
			Url:      server.URL,
		}, nil
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected HTTP failure")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %v, want HTTP status", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination stat error = %v, want not exist", statErr)
	}
}

func TestArtifactsDownloadCleansUpRequestErrors(t *testing.T) {
	restoreArtifactAPIs(t)

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt"},
			Url:      "https://example.test/artifact",
		}, nil
	}
	ciArtifactDownloadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		}),
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected request failure")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Fatalf("error = %v, want request error", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination stat error = %v, want not exist", statErr)
	}
}

func TestArtifactsDownloadUsesBoundedHTTPClient(t *testing.T) {
	if ciArtifactDownloadClient == nil {
		t.Fatal("ciArtifactDownloadClient is nil")
	}
	if ciArtifactDownloadClient.Timeout != artifactDownloadHTTPTimeout {
		t.Fatalf("download HTTP timeout = %s, want %s", ciArtifactDownloadClient.Timeout, artifactDownloadHTTPTimeout)
	}
}

func TestArtifactsDownloadCleansUpPartialBodyCopyErrors(t *testing.T) {
	restoreArtifactAPIs(t)

	destination := filepath.Join(t.TempDir(), "coverage.txt")
	ciGetArtifactDownloadURL = func(ctx context.Context, token, orgID, artifactID string) (*civ1.GetArtifactDownloadURLResponse, error) {
		return &civ1.GetArtifactDownloadURLResponse{
			Artifact: &civ1.Artifact{ArtifactId: artifactID, Name: "coverage.txt"},
			Url:      "https://example.test/artifact",
		}, nil
	}
	ciArtifactDownloadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &failingReadCloser{},
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	cmd := NewCmdArtifactsDownload()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output-file", destination, "artifact-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected copy failure")
	}
	if !strings.Contains(err.Error(), "copy failed") {
		t.Fatalf("error = %v, want copy error", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination stat error = %v, want not exist", statErr)
	}
}

func TestArtifactLocalFilenameSanitizesName(t *testing.T) {
	if got, want := artifactLocalFilename("../logs/output.txt", "artifact-1"), ".._logs_output.txt"; got != want {
		t.Fatalf("artifactLocalFilename = %q, want %q", got, want)
	}
	if got, want := artifactLocalFilename("...", "artifact-1"), "artifact-artifact-1"; got != want {
		t.Fatalf("artifactLocalFilename blank = %q, want %q", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type failingReadCloser struct {
	sent bool
}

func (r *failingReadCloser) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("copy failed")
	}
	r.sent = true
	return copy(p, "partial artifact bytes"), errors.New("copy failed")
}

func (r *failingReadCloser) Close() error {
	return nil
}

func testArtifact(id, name string) *civ1.Artifact {
	return &civ1.Artifact{
		ArtifactId:   id,
		RunId:        "run-123",
		WorkflowId:   "workflow-123",
		WorkflowPath: ".depot/build.yml",
		JobId:        "job-123",
		JobKey:       "build",
		AttemptId:    "attempt-123",
		Attempt:      1,
		Name:         name,
		SizeBytes:    1536,
		CreatedAt:    "2026-05-11T12:00:00Z",
	}
}
