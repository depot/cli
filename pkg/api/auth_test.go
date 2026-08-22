package api

import (
	"reflect"
	"testing"
)

func TestBrowserCommandWindowsPreservesURLArgument(t *testing.T) {
	t.Parallel()

	destination := "https://depot.dev/orgs/org-123/usage?section=ci&range=month"
	cmd, args, err := browserCommand("windows", destination)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "rundll32" {
		t.Fatalf("command = %q, want rundll32", cmd)
	}
	wantArgs := []string{"url.dll,FileProtocolHandler", destination}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}
