package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CopyMode controls behavior when destination already exists
type CopyMode int

const (
	CopyModeError     CopyMode = iota // Error if dest exists
	CopyModeOverwrite                 // Overwrite existing files
)

// CopyResult describes what was copied
type CopyResult struct {
	FilesCopied []string
	DirsCreated []string
	Warnings    []string
}

// CopyGitHubToDepot copies specified subdirectories from .github/ to .depot/
// repoRoot is the repository root. dirs are subdirectory names like "workflows", "actions".
func CopyGitHubToDepot(repoRoot string, dirs []string, mode CopyMode) (*CopyResult, error) {
	result := &CopyResult{
		FilesCopied: []string{},
		DirsCreated: []string{},
		Warnings:    []string{},
	}

	// 1. Verify repoRoot/.github/ exists
	githubPath := filepath.Join(repoRoot, ".github")
	githubInfo, err := os.Stat(githubPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".github directory does not exist at %s", githubPath)
		}
		return nil, fmt.Errorf("failed to stat .github directory: %w", err)
	}
	if !githubInfo.IsDir() {
		return nil, fmt.Errorf(".github is not a directory at %s", githubPath)
	}

	// 2. Check if repoRoot/.depot/ exists
	depotPath := filepath.Join(repoRoot, ".depot")
	depotInfo, err := os.Stat(depotPath)
	if err == nil {
		// .depot exists
		if !depotInfo.IsDir() {
			return nil, fmt.Errorf(".depot exists but is not a directory at %s", depotPath)
		}
		if mode == CopyModeError {
			return nil, fmt.Errorf(".depot directory already exists at %s", depotPath)
		}
		// CopyModeOverwrite: proceed
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to stat .depot directory: %w", err)
	}
	// If .depot doesn't exist, it will be created on demand

	// 3. For each dir in dirs
	for _, dir := range dirs {
		srcDir := filepath.Join(githubPath, dir)

		// 3a. Check if .github/{dir}/ exists
		srcInfo, err := os.Stat(srcDir)
		if err != nil {
			if os.IsNotExist(err) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("source directory does not exist: .github/%s", dir))
				continue
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("failed to stat .github/%s: %v", dir, err))
			continue
		}
		if !srcInfo.IsDir() {
			result.Warnings = append(result.Warnings, fmt.Sprintf(".github/%s is not a directory", dir))
			continue
		}

		// 3b. Walk .github/{dir}/ using filepath.WalkDir
		err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}

			// Get relative path from srcDir
			relPath, err := filepath.Rel(srcDir, path)
			if err != nil {
				return err
			}

			// Destination path
			destPath := filepath.Join(depotPath, dir, relPath)

			// 3c. Handle different entry types
			if d.IsDir() {
				// Skip directories (create them on demand when copying files)
				return nil
			}

			// Check for symlinks
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}

			if info.Mode()&os.ModeSymlink != 0 {
				// Skip symlinks and add warning
				result.Warnings = append(result.Warnings, fmt.Sprintf("skipped symlink: %s", relPath))
				return nil
			}

			// Copy regular files
			// Create parent directories
			destDir := filepath.Dir(destPath)
			if err := os.MkdirAll(destDir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", destDir, err)
			}

			// Track directory creation
			if _, err := os.Stat(destDir); err == nil {
				// Directory exists, check if we already tracked it
				alreadyTracked := false
				for _, tracked := range result.DirsCreated {
					if tracked == destDir {
						alreadyTracked = true
						break
					}
				}
				if !alreadyTracked {
					result.DirsCreated = append(result.DirsCreated, destDir)
				}
			}

			// Copy file
			srcFile, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open source file %s: %w", path, err)
			}
			defer srcFile.Close()

			destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
			}
			defer destFile.Close()

			if _, err := io.Copy(destFile, srcFile); err != nil {
				return fmt.Errorf("failed to copy file %s to %s: %w", path, destPath, err)
			}

			// Track file copy
			result.FilesCopied = append(result.FilesCopied, destPath)

			return nil
		})

		if err != nil {
			return nil, fmt.Errorf("failed to walk directory .github/%s: %w", dir, err)
		}
	}

	return result, nil
}
