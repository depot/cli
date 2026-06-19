package tests

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

const (
	maxReportFiles      = 1000
	maxReportFileBytes  = 50 * 1024 * 1024
	maxReportTotalBytes = 100 * 1024 * 1024
)

type discoveredReportFile struct {
	absolutePath string
	realPath     string
	filename     string
	info         os.FileInfo
}

func discoverReportFiles(input, workspace string) ([]discoveredReportFile, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}

	patterns, err := readCandidates(strings.NewReader(input))
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	var files []discoveredReportFile
	for _, pattern := range patterns {
		matches, err := expandReportPattern(pattern, workspace)
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

	searchRoot := findReportSearchRoot(absoluteInput)
	realSearchRoot, err := realExistingPath(searchRoot)
	if err != nil {
		return nil, err
	}
	if !isInsideWorkspace(workspace, realSearchRoot) {
		return nil, fmt.Errorf("test report path is outside the workspace: %s", input)
	}

	if stat, err := os.Stat(absoluteInput); err == nil && stat.IsDir() {
		realInput, err := filepath.EvalSymlinks(absoluteInput)
		if err != nil {
			return nil, err
		}
		if realInput == workspace {
			return nil, fmt.Errorf("test report path is too broad; use a report subdirectory instead: %s", input)
		}
		return walkXMLFiles(realInput)
	}

	if hasRecursiveGlob(absoluteInput) {
		if realSearchRoot == workspace {
			return nil, fmt.Errorf("test report path is too broad; use a report subdirectory instead: %s", input)
		}
		return walkRecursiveGlob(absoluteInput, realSearchRoot)
	}

	matches, err := filepath.Glob(absoluteInput)
	if err != nil {
		return nil, err
	}
	return matches, nil
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
	if info.Mode()&os.ModeSymlink != 0 {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report file must not be a symlink: %s", match)
	}
	if strings.ToLower(filepath.Ext(absolutePath)) != ".xml" {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report file must have a .xml extension: %s", match)
	}
	if !info.Mode().IsRegular() {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report is not a regular file: %s", match)
	}
	if info.Size() > maxReportFileBytes {
		return discoveredReportFile{}, false, fmt.Errorf("test report file %s exceeds the %s per-file limit", match, formatBytes(maxReportFileBytes))
	}

	realPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	if !isInsideWorkspace(workspace, realPath) {
		return discoveredReportFile{}, false, fmt.Errorf("matched test report file is outside the workspace: %s", match)
	}

	filename, err := filepath.Rel(workspace, realPath)
	if err != nil {
		return discoveredReportFile{}, false, err
	}
	return discoveredReportFile{
		absolutePath: absolutePath,
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

		contents, err := readReportFile(file.absolutePath)
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

func readReportFile(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(io.LimitReader(file, maxReportFileBytes+1))
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

func walkXMLFiles(root string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if strings.ToLower(filepath.Ext(filePath)) == ".xml" {
			matches = append(matches, filePath)
		}
		return nil
	})
	return matches, err
}

func walkRecursiveGlob(pattern, root string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		ok, err := matchRecursivePattern(pattern, filePath)
		if err != nil {
			return err
		}
		if ok {
			matches = append(matches, filePath)
		}
		return nil
	})
	return matches, err
}

func matchRecursivePattern(pattern, filePath string) (bool, error) {
	patternParts := splitSlashPath(filepath.ToSlash(filepath.Clean(pattern)))
	pathParts := splitSlashPath(filepath.ToSlash(filepath.Clean(filePath)))
	return matchRecursiveParts(patternParts, pathParts)
}

func matchRecursiveParts(patternParts, pathParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(pathParts) == 0, nil
	}

	part := patternParts[0]
	if part == "**" {
		for i := 0; i <= len(pathParts); i++ {
			ok, err := matchRecursiveParts(patternParts[1:], pathParts[i:])
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}

	if len(pathParts) == 0 {
		return false, nil
	}
	ok, err := path.Match(part, pathParts[0])
	if err != nil || !ok {
		return ok, err
	}
	return matchRecursiveParts(patternParts[1:], pathParts[1:])
}

func splitSlashPath(value string) []string {
	parts := strings.Split(value, "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func findReportSearchRoot(pattern string) string {
	volume := filepath.VolumeName(pattern)
	rest := strings.TrimPrefix(pattern, volume)
	root := volume + string(filepath.Separator)
	if !filepath.IsAbs(pattern) {
		root = "."
	}

	segments := strings.Split(strings.Trim(rest, string(filepath.Separator)), string(filepath.Separator))
	current := root
	for _, segment := range segments {
		if segment == "" || hasGlobPatternCharacter(segment) {
			break
		}
		current = filepath.Join(current, segment)
	}
	return current
}

func realExistingPath(filePath string) (string, error) {
	current := filepath.Clean(filePath)
	for {
		realPath, err := filepath.EvalSymlinks(current)
		if err == nil {
			return realPath, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("unable to resolve test report path: %s", filePath)
		}
		current = parent
	}
}

func hasGlobPatternCharacter(segment string) bool {
	return strings.ContainsAny(segment, "*?[")
}

func hasRecursiveGlob(pattern string) bool {
	for _, part := range strings.Split(filepath.ToSlash(pattern), "/") {
		if part == "**" {
			return true
		}
	}
	return false
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
