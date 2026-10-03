package api

import (
	"slices"
	"testing"
)

func TestOpenURLCommandPassesTheWholeURLAsOneArgument(t *testing.T) {
	url := "https://auth.example.com/authorize?client_id=a&state=b&redirect_uri=c"
	for _, goos := range []string{"windows", "darwin", "linux"} {
		name, args, err := openURLCommand(goos, url)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if name == "cmd" {
			t.Fatalf("%s: cmd.exe splits URLs at &", goos)
		}
		if !slices.Contains(args, url) {
			t.Fatalf("%s: args %q do not carry the URL intact", goos, args)
		}
	}
	if _, _, err := openURLCommand("plan9", url); err == nil {
		t.Fatal("expected an error for an unsupported platform")
	}
}
