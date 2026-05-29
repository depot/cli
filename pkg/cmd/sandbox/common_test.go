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
