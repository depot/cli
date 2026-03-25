package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initBareRemote creates a bare git repo with one commit and returns its path.
func initBareRemote(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	run(t, bare, "git", "init", "--bare")

	// Create a temp clone, add a commit, push to bare
	clone := t.TempDir()
	run(t, clone, "git", "clone", bare, ".")
	run(t, clone, "git", "config", "user.email", "test@test.com")
	run(t, clone, "git", "config", "user.name", "test")
	writeFile(t, filepath.Join(clone, "README.md"), "init")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "initial")
	run(t, clone, "git", "push", "origin", "HEAD")

	return bare
}

// cloneRepo clones the bare remote into a temp dir and returns the clone path.
func cloneRepo(t *testing.T, bare string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", bare, ".")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "test")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFindMergeBase_PushedBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create and push a feature branch
	run(t, clone, "git", "checkout", "-b", "feature/test")
	writeFile(t, filepath.Join(clone, "feature.txt"), "feature work")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "feature commit")
	run(t, clone, "git", "push", "-u", "origin", "feature/test")

	// Add a local-only change (not pushed)
	writeFile(t, filepath.Join(clone, "local.txt"), "local only")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "local commit")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	if baseBranch != "origin/feature/test" {
		t.Errorf("expected baseBranch=origin/feature/test, got %q", baseBranch)
	}

	// The merge base should be the SHA of origin/feature/test (the pushed commit)
	expectedSHA := run(t, clone, "git", "rev-parse", "origin/feature/test")
	if mergeBase != expectedSHA {
		t.Errorf("expected mergeBase=%s, got %s", expectedSHA, mergeBase)
	}
}

func TestFindMergeBase_UnpushedBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Create a local-only branch (not pushed)
	run(t, clone, "git", "checkout", "-b", "local-only-branch")
	writeFile(t, filepath.Join(clone, "local.txt"), "local work")
	run(t, clone, "git", "add", ".")
	run(t, clone, "git", "commit", "-m", "local commit")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	// Should fall back to default branch
	defaultBranch := run(t, clone, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	defaultBranch = strings.TrimPrefix(defaultBranch, "refs/remotes/")
	if baseBranch != defaultBranch {
		t.Errorf("expected baseBranch=%s, got %q", defaultBranch, baseBranch)
	}

	// Merge base should be against origin/main (or whatever the default is)
	expectedSHA := run(t, clone, "git", "merge-base", "HEAD", defaultBranch)
	if mergeBase != expectedSHA {
		t.Errorf("expected mergeBase=%s, got %s", expectedSHA, mergeBase)
	}
}


func TestFindMergeBase_OnDefaultBranch(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Add a local commit on main branch
	run(t, clone, "git", "commit", "--allow-empty", "-m", "local commit on main")

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	// Should follow "pushed branch" path and return origin/main as base
	if baseBranch != "origin/main" {
		t.Errorf("expected baseBranch=origin/main, got %s", baseBranch)
	}

	// Merge base should be origin/main SHA, not HEAD (since we added a local commit)
	originMainSHA := run(t, clone, "git", "rev-parse", "origin/main")
	if mergeBase != originMainSHA {
		t.Errorf("expected mergeBase=%s (origin/main), got %s", originMainSHA, mergeBase)
	}

	// Verify HEAD is different (has the local commit)
	headSHA := run(t, clone, "git", "rev-parse", "HEAD")
	if headSHA == originMainSHA {
		t.Error("HEAD should differ from origin/main after local commit")
	}
}


func TestFindMergeBase_DetachedHEAD(t *testing.T) {
	bare := initBareRemote(t)
	clone := cloneRepo(t, bare)

	// Detach HEAD
	headSHA := run(t, clone, "git", "rev-parse", "HEAD")
	run(t, clone, "git", "checkout", headSHA)

	baseBranch, mergeBase, err := findMergeBase(clone)
	if err != nil {
		t.Fatalf("findMergeBase failed: %v", err)
	}

	// Detached HEAD: should fall back to default branch merge base
	if mergeBase != headSHA {
		t.Errorf("expected mergeBase=%s, got %s", headSHA, mergeBase)
	}

	_ = baseBranch
}
