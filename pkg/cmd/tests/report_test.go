package tests

import (
	"bytes"
	"compress/gzip"
	"fmt"
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

	files, err := discoverReportFiles([]string{"reports/b.xml", "reports/a.xml"}, workspace)
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

	files, err := discoverReportFiles([]string{"reports"}, workspace)
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

	files, err := discoverReportFiles([]string{"reports/**/*.xml"}, workspace)
	if err != nil {
		t.Fatal(err)
	}

	got := filenames(files)
	want := []string{"reports/a.xml", "reports/nested/b.xml"}
	if !equalStrings(got, want) {
		t.Fatalf("expected files %v, got %v", want, got)
	}
}

func TestDiscoverReportFilesDeduplicatesMatches(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")

	files, err := discoverReportFiles([]string{"reports/a.xml", "reports/*.xml"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := filenames(files); !equalStrings(got, []string{"reports/a.xml"}) {
		t.Fatalf("expected deduplicated file list, got %v", got)
	}
}

func TestDiscoverReportFilesRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	outside := writeTempFile(t, "outside.xml", "<testsuite/>")

	_, err := discoverReportFiles([]string{outside}, workspace)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected outside workspace error, got %v", err)
	}

	_, err = discoverReportFiles([]string{"**/*.xml"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected broad glob error, got %v", err)
	}

	_, err = discoverReportFiles([]string{"{**/*.xml,reports/*.xml}"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected broad brace glob error, got %v", err)
	}

	_, err = discoverReportFiles([]string{"."}, workspace)
	if err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected broad directory error, got %v", err)
	}
}

func TestDiscoverReportFilesRejectsNonXMLAndFollowsSafeFileSymlink(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/out.txt", "not xml")

	_, err := discoverReportFiles([]string{"reports/out.txt"}, workspace)
	if err == nil || !strings.Contains(err.Error(), ".xml extension") {
		t.Fatalf("expected non-XML error, got %v", err)
	}

	target := writeTempFileAt(t, workspace, "reports/target.xml", "<testsuite/>")
	link := filepath.Join(workspace, "reports/link.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	files, err := discoverReportFiles([]string{"reports/link.xml"}, workspace)
	if err != nil {
		t.Fatalf("expected safe file symlink to be accepted, got %v", err)
	}
	if got := filenames(files); !equalStrings(got, []string{"reports/target.xml"}) {
		t.Fatalf("expected symlink to resolve to real workspace file, got %v", got)
	}
}

func TestDiscoverReportFilesFollowsSafeFileSymlinkFromDirectory(t *testing.T) {
	workspace := t.TempDir()
	target := writeTempFileAt(t, workspace, "real/junit.xml", "<testsuite/>")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "reports/link.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	files, err := discoverReportFiles([]string{"reports"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := filenames(files); !equalStrings(got, []string{"real/junit.xml"}) {
		t.Fatalf("expected directory discovery to include safe symlinked file, got %v", got)
	}
}

func TestDiscoverReportFilesFollowsSafeFileSymlinkFromRecursiveGlob(t *testing.T) {
	workspace := t.TempDir()
	target := writeTempFileAt(t, workspace, "real/junit.xml", "<testsuite/>")
	if err := os.MkdirAll(filepath.Join(workspace, "reports/nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "reports/nested/link.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	files, err := discoverReportFiles([]string{"reports/**/*.xml"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := filenames(files); !equalStrings(got, []string{"real/junit.xml"}) {
		t.Fatalf("expected recursive glob discovery to include safe symlinked file, got %v", got)
	}
}

func TestDiscoverReportFilesFollowsSafeDirectorySymlinkFromRecursiveGlob(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "real/nested/junit.xml", "<testsuite/>")
	link := filepath.Join(workspace, "reports")
	if err := os.Symlink(filepath.Join(workspace, "real"), link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	files, err := discoverReportFiles([]string{"reports/**/*.xml"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := filenames(files); !equalStrings(got, []string{"real/nested/junit.xml"}) {
		t.Fatalf("expected recursive glob discovery through safe symlinked directory, got %v", got)
	}
}

func TestDiscoverReportFilesRejectsFileSymlinkOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	target := writeTempFileAt(t, outside, "target.xml", "<testsuite/>")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "reports/link.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := discoverReportFiles([]string{"reports/link.xml"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected symlink outside workspace error, got %v", err)
	}
}

func TestDiscoverReportFilesRejectsSymlinkDirectoryOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	writeTempFileAt(t, outside, "junit.xml", "<testsuite/>")
	link := filepath.Join(workspace, "reports")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := discoverReportFiles([]string{"reports"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected symlink directory outside workspace error, got %v", err)
	}
}

func TestDiscoverReportFilesRejectsRecursiveGlobThroughSymlinkDirectoryOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	writeTempFileAt(t, outside, "nested/junit.xml", "<testsuite/>")
	link := filepath.Join(workspace, "reports")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := discoverReportFiles([]string{"reports/**/*.xml"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected symlink directory outside workspace error, got %v", err)
	}
}

func TestDiscoverReportFilesEnforcesCountLimitDuringDirectoryWalk(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i <= maxReportFiles; i++ {
		writeTempFileAt(t, workspace, filepath.Join("reports", fmt.Sprintf("%04d.xml", i)), "<testsuite/>")
	}

	_, err := discoverReportFiles([]string{"reports"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "matched more than") {
		t.Fatalf("expected discovery count limit error, got %v", err)
	}
}

func TestPrepareReportFilesGzipsXML(t *testing.T) {
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite><testcase name=\"a\"/></testsuite>")
	files, err := discoverReportFiles([]string{"reports/a.xml"}, workspace)
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
	files, err := discoverReportFiles([]string{"reports/a.xml"}, workspace)
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

func TestPrepareReportFilesRejectsPathSwapBeforeRead(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	reportPath := writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	outsidePath := writeTempFileAt(t, outside, "outside.xml", "<testsuite><testcase name=\"secret\"/></testsuite>")
	files, err := discoverReportFiles([]string{"reports/a.xml"}, workspace)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, reportPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err = prepareReportFiles(files)
	if err == nil || !strings.Contains(err.Error(), "changed after discovery") {
		t.Fatalf("expected path swap after discovery to be rejected, got %v", err)
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

func TestPrepareReportFilesRejectsAggregateSizeLimit(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"reports/a.xml", "reports/b.xml", "reports/c.xml"} {
		reportPath := writeTempFileAt(t, workspace, name, "")
		if err := os.Truncate(reportPath, 40*1024*1024); err != nil {
			t.Fatal(err)
		}
	}
	files, err := discoverReportFiles([]string{"reports"}, workspace)
	if err != nil {
		t.Fatal(err)
	}

	_, err = prepareReportFiles(files)
	if err == nil || !strings.Contains(err.Error(), "total uncompressed size limit") {
		t.Fatalf("expected aggregate size limit error, got %v", err)
	}
}

func TestPrepareReportFilesRejectsTooManyFiles(t *testing.T) {
	files := make([]discoveredReportFile, maxReportFiles+1)
	_, err := prepareReportFiles(files)
	if err == nil || !strings.Contains(err.Error(), "matched more than") {
		t.Fatalf("expected too many files error, got %v", err)
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
