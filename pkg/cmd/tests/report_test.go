package tests

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverReportFilesFindsExplicitXMLFiles(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/b.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")

	files, err := discoverReportFiles("reports/b.xml\nreports/a.xml", workspace)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].filename != "reports/a.xml" || files[1].filename != "reports/b.xml" {
		t.Fatalf("expected sorted relative filenames, got %q and %q", files[0].filename, files[1].filename)
	}
}

func TestDiscoverReportFilesExpandsDirectoryToXMLFiles(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/nested/b.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/notes.txt", "ignore")

	files, err := discoverReportFiles("reports", workspace)
	if err != nil {
		t.Fatal(err)
	}

	got := filenames(files)
	want := []string{"reports/a.xml", "reports/nested/b.xml"}
	if !equalStrings(got, want) {
		t.Fatalf("expected files %v, got %v", want, got)
	}
}

func TestDiscoverReportFilesSupportsRecursiveGlobBelowWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/nested/b.xml", "<testsuite/>")

	files, err := discoverReportFiles("reports/**/*.xml", workspace)
	if err != nil {
		t.Fatal(err)
	}

	got := filenames(files)
	want := []string{"reports/a.xml", "reports/nested/b.xml"}
	if !equalStrings(got, want) {
		t.Fatalf("expected files %v, got %v", want, got)
	}
}

func TestDiscoverReportFilesRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	outside := writeTempFile(t, "outside.xml", "<testsuite/>")

	_, err := discoverReportFiles(outside, workspace)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected outside workspace error, got %v", err)
	}

	_, err = discoverReportFiles("**/*.xml", workspace)
	if err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected broad glob error, got %v", err)
	}

	_, err = discoverReportFiles(".", workspace)
	if err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected broad directory error, got %v", err)
	}
}

func TestDiscoverReportFilesRejectsNonXMLAndSymlink(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/out.txt", "not xml")

	_, err := discoverReportFiles("reports/out.txt", workspace)
	if err == nil || !strings.Contains(err.Error(), ".xml extension") {
		t.Fatalf("expected non-XML error, got %v", err)
	}

	target := writeTempFileAt(t, workspace, "reports/target.xml", "<testsuite/>")
	link := filepath.Join(workspace, "reports/link.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err = discoverReportFiles("reports/link.xml", workspace)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPrepareReportFilesGzipsXML(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite><testcase name=\"a\"/></testsuite>")
	files, err := discoverReportFiles("reports/a.xml", workspace)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := prepareReportFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	if len(prepared) != 1 {
		t.Fatalf("expected 1 prepared file, got %d", len(prepared))
	}
	if prepared[0].GetFilename() != "reports/a.xml" {
		t.Fatalf("expected filename reports/a.xml, got %q", prepared[0].GetFilename())
	}

	reader, err := gzip.NewReader(bytes.NewReader(prepared[0].GetGzippedXml()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "<testcase name=\"a\"/>") {
		t.Fatalf("expected gzipped XML contents, got %q", string(contents))
	}
}

func TestPrepareReportFilesRejectsChangedFile(t *testing.T) {
	workspace := t.TempDir()
	reportPath := writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	files, err := discoverReportFiles("reports/a.xml", workspace)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite><testcase name=\"new\"/></testsuite>")

	_, err = prepareReportFiles(files)
	if err == nil || !strings.Contains(err.Error(), "changed after discovery") {
		t.Fatalf("expected changed file error, got %v", err)
	}
}

func TestPrepareReportFilesRejectsOversizedFile(t *testing.T) {
	workspace := t.TempDir()
	reportPath := writeTempFileAt(t, workspace, "reports/a.xml", "")
	if err := os.Truncate(reportPath, maxReportFileBytes+1); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	realPath, err := filepath.EvalSymlinks(reportPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = prepareReportFiles([]discoveredReportFile{
		{
			absolutePath: reportPath,
			realPath:     realPath,
			filename:     "reports/a.xml",
			info:         info,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "per-file limit") {
		t.Fatalf("expected per-file limit error, got %v", err)
	}
}

func writeTempFileAt(t *testing.T, root, name, contents string) string {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func filenames(files []discoveredReportFile) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.filename)
	}
	return names
}
