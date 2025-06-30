package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsGitRepository(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		cleanupFunc func(string)
		expected    bool
	}{
		{
			name: "valid git repository",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to initialize git repo: %v", err)
				}
				return tmpDir
			},
			expected: true,
		},
		{
			name: "not a git repository",
			setupFunc: func(t *testing.T) string {
				return t.TempDir()
			},
			expected: false,
		},
		{
			name: "nonexistent directory",
			setupFunc: func(t *testing.T) string {
				return "/nonexistent/directory"
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setupFunc(t)
			if tt.cleanupFunc != nil {
				defer tt.cleanupFunc(dir)
			}

			ctx := context.Background()
			result := isGitRepository(ctx, dir)

			if result != tt.expected {
				t.Errorf("isGitRepository() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCreateAndCheckoutBranch(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		branchName  string
		expectError bool
	}{
		{
			name: "create branch in valid git repo",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to initialize git repo: %v", err)
				}
				
				// Configure git user for the test
				cmd = exec.Command("git", "config", "user.email", "test@example.com")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user email: %v", err)
				}
				
				cmd = exec.Command("git", "config", "user.name", "Test User")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user name: %v", err)
				}
				
				// Create an initial commit
				testFile := filepath.Join(tmpDir, "test.txt")
				if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				
				cmd = exec.Command("git", "add", "test.txt")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to add test file: %v", err)
				}
				
				cmd = exec.Command("git", "commit", "-m", "Initial commit")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create initial commit: %v", err)
				}
				
				return tmpDir
			},
			branchName:  "test-branch",
			expectError: false,
		},
		{
			name: "create branch with special characters",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to initialize git repo: %v", err)
				}
				
				// Configure git user for the test
				cmd = exec.Command("git", "config", "user.email", "test@example.com")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user email: %v", err)
				}
				
				cmd = exec.Command("git", "config", "user.name", "Test User")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user name: %v", err)
				}
				
				// Create an initial commit
				testFile := filepath.Join(tmpDir, "test.txt")
				if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				
				cmd = exec.Command("git", "add", "test.txt")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to add test file: %v", err)
				}
				
				cmd = exec.Command("git", "commit", "-m", "Initial commit")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create initial commit: %v", err)
				}
				
				return tmpDir
			},
			branchName:  "claude-20240101-120000",
			expectError: false,
		},
		{
			name: "create branch in non-git directory",
			setupFunc: func(t *testing.T) string {
				return t.TempDir()
			},
			branchName:  "test-branch",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setupFunc(t)
			ctx := context.Background()

			err := createAndCheckoutBranch(ctx, dir, tt.branchName)

			if tt.expectError && err == nil {
				t.Errorf("createAndCheckoutBranch() expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("createAndCheckoutBranch() unexpected error: %v", err)
			}

			if !tt.expectError {
				// Verify the branch was created and checked out
				cmd := exec.Command("git", "branch", "--show-current")
				cmd.Dir = dir
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("Failed to get current branch: %v", err)
				}
				currentBranch := strings.TrimSpace(string(out))
				if currentBranch != tt.branchName {
					t.Errorf("Expected current branch %s, got %s", tt.branchName, currentBranch)
				}
			}
		})
	}
}

func TestHandleGitCleanup(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		sessionID   string
		expectError bool
	}{
		{
			name: "commit and push changes",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to initialize git repo: %v", err)
				}
				
				// Configure git user for the test
				cmd = exec.Command("git", "config", "user.email", "test@example.com")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user email: %v", err)
				}
				
				cmd = exec.Command("git", "config", "user.name", "Test User")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user name: %v", err)
				}
				
				// Create an initial commit
				testFile := filepath.Join(tmpDir, "test.txt")
				if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				
				cmd = exec.Command("git", "add", "test.txt")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to add test file: %v", err)
				}
				
				cmd = exec.Command("git", "commit", "-m", "Initial commit")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create initial commit: %v", err)
				}
				
				// Create a test branch
				cmd = exec.Command("git", "checkout", "-b", "test-branch")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create test branch: %v", err)
				}
				
				// Make some changes
				if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
					t.Fatalf("Failed to modify test file: %v", err)
				}
				
				return tmpDir
			},
			sessionID:   "test-session",
			expectError: false,
		},
		{
			name: "no changes to commit",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to initialize git repo: %v", err)
				}
				
				// Configure git user for the test
				cmd = exec.Command("git", "config", "user.email", "test@example.com")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user email: %v", err)
				}
				
				cmd = exec.Command("git", "config", "user.name", "Test User")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to configure git user name: %v", err)
				}
				
				// Create an initial commit
				testFile := filepath.Join(tmpDir, "test.txt")
				if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				
				cmd = exec.Command("git", "add", "test.txt")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to add test file: %v", err)
				}
				
				cmd = exec.Command("git", "commit", "-m", "Initial commit")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create initial commit: %v", err)
				}
				
				// Create a test branch
				cmd = exec.Command("git", "checkout", "-b", "test-branch")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatalf("Failed to create test branch: %v", err)
				}
				
				return tmpDir
			},
			sessionID:   "test-session",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setupFunc(t)
			ctx := context.Background()

			err := handleGitCleanup(ctx, dir, tt.sessionID)

			if tt.expectError && err == nil {
				t.Errorf("handleGitCleanup() expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("handleGitCleanup() unexpected error: %v", err)
			}
		})
	}
}

func TestGenerateSessionID(t *testing.T) {
	sessionID1 := generateSessionID()
	
	// Test that session IDs are generated
	if sessionID1 == "" {
		t.Error("generateSessionID() returned empty string")
	}

	// Test that session IDs have expected format
	if !strings.HasPrefix(sessionID1, "claude-") {
		t.Errorf("generateSessionID() = %s, expected to start with 'claude-'", sessionID1)
	}

	// Test that session IDs are unique (wait to ensure different timestamp)
	time.Sleep(1 * time.Second)
	sessionID2 := generateSessionID()
	if sessionID1 == sessionID2 {
		t.Errorf("generateSessionID() generated duplicate IDs: %s", sessionID1)
	}

	// Test format matches expected pattern (claude-YYYYMMDD-HHMMSS)
	parts := strings.Split(sessionID1, "-")
	if len(parts) != 3 {
		t.Errorf("generateSessionID() = %s, expected format 'claude-YYYYMMDD-HHMMSS'", sessionID1)
	}
	
	if parts[0] != "claude" {
		t.Errorf("generateSessionID() = %s, expected to start with 'claude'", sessionID1)
	}
	
	if len(parts[1]) != 8 {
		t.Errorf("generateSessionID() date part = %s, expected 8 characters", parts[1])
	}
	
	if len(parts[2]) != 6 {
		t.Errorf("generateSessionID() time part = %s, expected 6 characters", parts[2])
	}
}

func TestConvertPathToProjectName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple path",
			path:     "/Users/billy/Work",
			expected: "-Users-billy-Work",
		},
		{
			name:     "path with dots",
			path:     "/Users/jacobwgillespie/.dotfiles",
			expected: "-Users-jacobwgillespie--dotfiles",
		},
		{
			name:     "current directory",
			path:     ".",
			expected: "-",
		},
		{
			name:     "path with spaces",
			path:     "/Users/test user/My Documents",
			expected: "-Users-test-user-My-Documents",
		},
		{
			name:     "root path",
			path:     "/",
			expected: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertPathToProjectName(tt.path)
			if result != tt.expected {
				t.Errorf("convertPathToProjectName(%s) = %s, want %s", tt.path, result, tt.expected)
			}
		})
	}
}

func TestCreateBranchIntegration(t *testing.T) {
	// Test the full integration of --create-branch logic
	tmpDir := t.TempDir()
	
	// Setup git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize git repo: %v", err)
	}
	
	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user email: %v", err)
	}
	
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user name: %v", err)
	}
	
	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	
	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add test file: %v", err)
	}
	
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create initial commit: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session-123"

	// Test: Check if it's a git repo
	if !isGitRepository(ctx, tmpDir) {
		t.Fatal("Expected directory to be recognized as git repository")
	}

	// Test: Create and checkout branch
	if err := createAndCheckoutBranch(ctx, tmpDir, sessionID); err != nil {
		t.Fatalf("Failed to create and checkout branch: %v", err)
	}

	// Verify branch was created and checked out
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tmpDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	
	currentBranch := strings.TrimSpace(string(out))
	if currentBranch != sessionID {
		t.Errorf("Expected current branch %s, got %s", sessionID, currentBranch)
	}

	// Make some changes
	if err := os.WriteFile(testFile, []byte("modified by claude"), 0644); err != nil {
		t.Fatalf("Failed to modify test file: %v", err)
	}

	// Test: Handle git cleanup
	if err := handleGitCleanup(ctx, tmpDir, sessionID); err != nil {
		t.Fatalf("Failed to handle git cleanup: %v", err)
	}

	// Verify changes were committed
	cmd = exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = tmpDir
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get git log: %v", err)
	}
	
	commitMsg := string(out)
	expectedMsg := fmt.Sprintf("Claude session %s changes", sessionID)
	if !strings.Contains(commitMsg, expectedMsg) {
		t.Errorf("Expected commit message to contain %s, got %s", expectedMsg, commitMsg)
	}
}