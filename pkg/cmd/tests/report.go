package tests

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const (
	maxReportFiles       = 1000
	maxReportFileBytes   = 50 * 1024 * 1024
	maxReportTotalBytes  = 100 * 1024 * 1024
	reportRequestTimeout = 2 * time.Minute
)

type discoveredReportFile struct {
	absolutePath string
	realPath     string
	filename     string
	info         os.FileInfo
}

func uploadTestReportsResponse(cmd *cobra.Command, opts reportOptions) (int, *testresultsv1.ReportTestResultsResponse, error) {
	workspace, err := reportWorkspace()
	if err != nil {
		return 0, nil, err
	}

	files, err := discoverReportFiles(opts.reportPaths, workspace)
	if err != nil {
		return 0, nil, err
	}
	if len(files) == 0 {
		return 0, nil, fmt.Errorf("no JUnit XML report files matched")
	}

	prepared, err := prepareReportFiles(files)
	if err != nil {
		return 0, nil, err
	}

	requestCtx, cancel := context.WithTimeout(cmd.Context(), reportRequestTimeout)
	defer cancel()

	token, err := resolveOIDCCredentialFunc(requestCtx)
	if err != nil {
		return 0, nil, err
	}

	resp, err := reportTestResultsFunc(requestCtx, token, &testresultsv1.ReportTestResultsRequest{
		InvocationId: testKey(opts.key),
		Files:        prepared,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("failed to upload test reports: %w", err)
	}
	return len(prepared), resp, nil
}

func reportWorkspace() (string, error) {
	if workspace := os.Getenv("GITHUB_WORKSPACE"); strings.TrimSpace(workspace) != "" {
		return workspace, nil
	}
	return os.Getwd()
}

func splitReportPathInputs(inputs []string) []string {
	var paths []string
	for _, input := range inputs {
		normalized := strings.ReplaceAll(input, "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
		for _, path := range strings.Split(normalized, "\n") {
			path = strings.TrimSpace(path)
			if path != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func discoverReportFiles(inputs []string, workspace string) ([]discoveredReportFile, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	var files []discoveredReportFile
	for _, input := range splitReportPathInputs(inputs) {
		matches, err := expandReportPattern(input, workspace)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			file, ok, err := reportFileFromMatch(match, workspace)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if _, exists := seen[file.realPath]; exists {
				continue
			}
			seen[file.realPath] = struct{}{}
			files = append(files, file)
			if len(files) > maxReportFiles {
				return nil, fmt.Errorf("matched more than %d test report files", maxReportFiles)
			}
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].filename < files[j].filename
	})
	return files, nil
}

func expandReportPattern(input, workspace string) ([]string, error) {
	absoluteInput := input
	if !filepath.IsAbs(absoluteInput) {
		absoluteInput = filepath.Join(workspace, input)
	}
	absoluteInput = filepath.Clean(absoluteInput)
	inputHasGlob := hasGlobMeta(absoluteInput)

	if stat, err := os.Stat(absoluteInput); err == nil && stat.IsDir() {
		realInput, err := filepath.EvalSymlinks(absoluteInput)
		if err != nil {
			return nil, err
		}
		if !isInsideWorkspace(workspace, realInput) {
			return nil, fmt.Errorf("test report path is outside the workspace: %s", input)
		}
		if realInput == workspace {
			return nil, fmt.Errorf("test report path is too broad; use a report subdirectory instead: %s", input)
		}
		return globReportFiles(filepath.Join(realInput, "**", "*.xml"), true)
	}

	searchRoot := findReportSearchRoot(absoluteInput)
	realSearchRoot, err := realExistingPath(searchRoot)
	if err != nil {
		return nil, err
	}
	if !isInsideWorkspace(workspace, realSearchRoot) {
		return nil, fmt.Errorf("test report path is outside the workspace: %s", input)
	}

	if realSearchRoot == workspace && hasRecursiveGlob(absoluteInput) {
		return nil, fmt.Errorf("test report path is too broad; use a report subdirectory instead: %s", input)
	}
	resolvedPattern, err := resolveGlobPattern(absoluteInput, searchRoot, realSearchRoot)
	if err != nil {
		return nil, err
	}
	return globReportFiles(resolvedPattern, inputHasGlob)
}

func resolveGlobPattern(pattern, searchRoot, realSearchRoot string) (string, error) {
	relativePattern, err := filepath.Rel(searchRoot, pattern)
	if err != nil {
		return "", err
	}
	return filepath.Join(realSearchRoot, relativePattern), nil
}

func reportFileFromMatch(match, workspace string) (discoveredReportFile, bool, error) {
	absolutePath, err := filepath.Abs(match)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	absolutePath = filepath.Clean(absolutePath)

	info, err := os.Lstat(absolutePath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	if info.IsDir() {
		return discoveredReportFile{}, false, nil
	}

	realPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	if !isInsideWorkspace(workspace, realPath) {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report file is outside the workspace: %s", match)
	}
	info, err = os.Lstat(realPath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	if strings.ToLower(filepath.Ext(realPath)) != ".xml" {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report file must have a .xml extension: %s", match)
	}
	if !info.Mode().IsRegular() {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report is not a regular file: %s", match)
	}
	if info.Size() > maxReportFileBytes {
		return discoveredReportFile{}, false, fmt.Errorf("test report file %s exceeds the %s per-file limit", match, formatBytes(maxReportFileBytes))
	}

	filename, err := filepath.Rel(workspace, realPath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	return discoveredReportFile{
		absolutePath: realPath,
		realPath:     realPath,
		filename:     filepath.ToSlash(filename),
		info:         info,
	}, true, nil
}

func prepareReportFiles(files []discoveredReportFile) ([]*testresultsv1.TestResultsFile, error) {
	if len(files) > maxReportFiles {
		return nil, fmt.Errorf("matched more than %d test report files", maxReportFiles)
	}

	var totalBytes int64
	prepared := make([]*testresultsv1.TestResultsFile, 0, len(files))
	for _, file := range files {
		currentRealPath, err := filepath.EvalSymlinks(file.absolutePath)
		if err != nil {
			return nil, err
		}
		if currentRealPath != file.realPath {
			return nil, fmt.Errorf("test report file changed after discovery: %s", file.filename)
		}

		info, err := os.Lstat(file.absolutePath)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("matched test report is not a regular file: %s", file.filename)
		}
		if file.info != nil && reportFileChanged(file.info, info) {
			return nil, fmt.Errorf("test report file changed after discovery: %s", file.filename)
		}
		if info.Size() > maxReportFileBytes {
			return nil, fmt.Errorf("test report file %s exceeds the %s per-file limit", file.filename, formatBytes(maxReportFileBytes))
		}
		if totalBytes+info.Size() > maxReportTotalBytes {
			return nil, fmt.Errorf("matched test reports exceed the %s total uncompressed size limit", formatBytes(maxReportTotalBytes))
		}

		contents, err := readReportFile(file)
		if err != nil {
			return nil, err
		}
		if len(contents) > maxReportFileBytes {
			return nil, fmt.Errorf("test report file %s exceeds the %s per-file limit", file.filename, formatBytes(maxReportFileBytes))
		}
		totalBytes += int64(len(contents))
		if totalBytes > maxReportTotalBytes {
			return nil, fmt.Errorf("matched test reports exceed the %s total uncompressed size limit", formatBytes(maxReportTotalBytes))
		}

		gzipped, err := gzipBytes(contents)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, &testresultsv1.TestResultsFile{
			Filename:   file.filename,
			GzippedXml: gzipped,
		})
	}
	return prepared, nil
}

func reportFileChanged(discovered, current os.FileInfo) bool {
	return !os.SameFile(discovered, current) ||
		discovered.Size() != current.Size() ||
		discovered.Mode() != current.Mode() ||
		!discovered.ModTime().Equal(current.ModTime())
}

func readReportFile(reportFile discoveredReportFile) ([]byte, error) {
	file, err := os.Open(reportFile.absolutePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("matched test report is not a regular file: %s", reportFile.filename)
	}
	if reportFile.info != nil && reportFileChanged(reportFile.info, info) {
		return nil, fmt.Errorf("test report file changed after discovery: %s", reportFile.filename)
	}
	if info.Size() > maxReportFileBytes {
		return nil, fmt.Errorf("test report file %s exceeds the %s per-file limit", reportFile.filename, formatBytes(maxReportFileBytes))
	}

	contents, err := io.ReadAll(io.LimitReader(file, maxReportFileBytes+1))
	if err != nil {
		return nil, err
	}

	afterInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if reportFileChanged(info, afterInfo) {
		return nil, fmt.Errorf("test report file changed while reading: %s", reportFile.filename)
	}

	return contents, nil
}

func gzipBytes(contents []byte) ([]byte, error) {
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(contents); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func globReportFiles(pattern string, xmlOnly bool) ([]string, error) {
	base, filePattern := doublestar.SplitPattern(filepath.ToSlash(filepath.Clean(pattern)))
	base = filepath.FromSlash(base)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}

	var matches []string
	err := doublestar.GlobWalk(os.DirFS(base), filePattern, func(filePath string, entry fs.DirEntry) error {
		if entry.IsDir() {
			return nil
		}
		if xmlOnly && strings.ToLower(filepath.Ext(filePath)) != ".xml" {
			return nil
		}
		matches = append(matches, filepath.Join(base, filePath))
		if len(matches) > maxReportFiles {
			return fmt.Errorf("matched more than %d test report files", maxReportFiles)
		}
		return nil
	}, doublestar.WithFailOnIOErrors(), doublestar.WithFilesOnly(), doublestar.WithNoFollow())
	return matches, err
}

func findReportSearchRoot(pattern string) string {
	base, _ := doublestar.SplitPattern(filepath.ToSlash(filepath.Clean(pattern)))
	return filepath.FromSlash(base)
}

func realExistingPath(filePath string) (string, error) {
	current := filepath.Clean(filePath)
	var missing []string
	for {
		realPath, err := filepath.EvalSymlinks(current)
		if err == nil {
			for _, part := range missing {
				realPath = filepath.Join(realPath, part)
			}
			return realPath, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("unable to resolve test report path: %s", filePath)
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func hasRecursiveGlob(pattern string) bool {
	for _, part := range strings.Split(filepath.ToSlash(pattern), "/") {
		if strings.Contains(part, "**") {
			return true
		}
	}
	return false
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(filepath.ToSlash(pattern), "*?{[")
}

func isInsideWorkspace(workspace, filePath string) bool {
	rel, err := filepath.Rel(workspace, filePath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel))
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	kib := float64(bytes) / 1024
	if kib < 1024 {
		return fmt.Sprintf("%.1f KiB", kib)
	}
	return fmt.Sprintf("%.1f MiB", kib/1024)
}
