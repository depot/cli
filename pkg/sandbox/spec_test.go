package sandbox

import (
	"strings"
	"testing"
)

func TestResolveHooksShellQuotesInputs(t *testing.T) {
	spec := &Spec{
		On: HooksSpec{
			Exec: []HookSpec{{Command: "echo ${input.value}"}},
		},
	}

	hooks, err := spec.ResolveHooks(map[string]string{
		"value": "hello; touch /tmp/pwned",
	})
	if err != nil {
		t.Fatalf("ResolveHooks returned error: %v", err)
	}

	got := hooks.Exec[0].Command
	want := "echo 'hello; touch /tmp/pwned'"
	if got != want {
		t.Fatalf("resolved command = %q, want %q", got, want)
	}
}

func TestResolveHooksEscapesSingleQuotes(t *testing.T) {
	spec := &Spec{
		On: HooksSpec{
			Exec: []HookSpec{{Command: "printf %s ${input.value}"}},
		},
	}

	hooks, err := spec.ResolveHooks(map[string]string{
		"value": "can't",
	})
	if err != nil {
		t.Fatalf("ResolveHooks returned error: %v", err)
	}

	got := hooks.Exec[0].Command
	want := "printf %s 'can'\\''t'"
	if got != want {
		t.Fatalf("resolved command = %q, want %q", got, want)
	}
}

func TestResolveHooksRejectsQuotedInputPlaceholders(t *testing.T) {
	for _, command := range []string{
		"echo '${input.value}'",
		"echo \"${input.value}\"",
	} {
		t.Run(command, func(t *testing.T) {
			spec := &Spec{
				On: HooksSpec{
					Exec: []HookSpec{{Command: command}},
				},
			}

			_, err := spec.ResolveHooks(map[string]string{
				"value": "attacker; touch /tmp/pwned",
			})
			if err == nil {
				t.Fatal("expected quoted placeholder to fail")
			}
			if !strings.Contains(err.Error(), "inside shell quotes") {
				t.Fatalf("error = %q, want inside shell quotes", err)
			}
		})
	}
}

func TestResolveHooksRejectsInvalidTimeoutSeconds(t *testing.T) {
	for name, timeout := range map[string]int{
		"negative":  -1,
		"too-large": maxHookTimeoutSeconds + 1,
	} {
		t.Run(name, func(t *testing.T) {
			spec := &Spec{
				On: HooksSpec{
					Exec: []HookSpec{{
						Command:        "true",
						TimeoutSeconds: timeout,
					}},
				},
			}

			_, err := spec.ResolveHooks(nil)
			if err == nil {
				t.Fatal("expected invalid timeout_seconds to fail")
			}
			if !strings.Contains(err.Error(), "timeout_seconds") {
				t.Fatalf("error = %q, want timeout_seconds", err)
			}
		})
	}
}
