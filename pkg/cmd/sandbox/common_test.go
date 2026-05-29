package sandbox

import (
	"testing"

	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
)

// Every sandbox id passed over the wire flows through sandboxRef(). This pins
// the wire shape so a future refactor cannot silently regress to a bare string
// id without the oneof envelope.
func TestSandboxRef_PinnedShape(t *testing.T) {
	r := sandboxRef("cs-abc123")
	if r == nil {
		t.Fatal("sandboxRef returned nil")
	}
	sel, ok := r.Selector.(*sandboxv1.SandboxRef_Id)
	if !ok {
		t.Fatalf("expected SandboxRef_Id selector, got %T", r.Selector)
	}
	if sel.Id != "cs-abc123" {
		t.Errorf("id = %q, want cs-abc123", sel.Id)
	}
}

func TestCommandRef_PinnedShape(t *testing.T) {
	r := commandRef("cmd-xyz789")
	if r == nil {
		t.Fatal("commandRef returned nil")
	}
	sel, ok := r.Selector.(*sandboxv1.SandboxCommandExecutionRef_Id)
	if !ok {
		t.Fatalf("expected CommandRef_Id selector, got %T", r.Selector)
	}
	if sel.Id != "cmd-xyz789" {
		t.Errorf("id = %q, want cmd-xyz789", sel.Id)
	}
}

func TestSnapshotRef_PinnedShape(t *testing.T) {
	r := snapshotRef("snap-456")
	if r == nil {
		t.Fatal("snapshotRef returned nil")
	}
	sel, ok := r.Selector.(*sandboxv1.SnapshotRef_Id)
	if !ok {
		t.Fatalf("expected SnapshotRef_Id selector, got %T", r.Selector)
	}
	if sel.Id != "snap-456" {
		t.Errorf("id = %q, want snap-456", sel.Id)
	}
}
