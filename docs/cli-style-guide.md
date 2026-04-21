# Depot CLI style guide

A descriptive, pattern-first guide to how `depot` CLI commands are shaped today.
The goal is to give anyone (human or agent) adding a new verb a single reference
so the surface stays consistent. When the existing code disagrees with itself, I
note the dominant pattern and the drift; I do **not** propose renames.

Grounded in a sweep of `pkg/cmd/**/*.go` as of April 2026.

---

## 1. The anchor example

When in doubt, copy the shape of one of these commands. They represent the
current "blessed" style end-to-end:

- `pkg/cmd/ci/status.go` — simplest positional-id read command.
- `pkg/cmd/ci/cancel.go` — positional id + mutually exclusive flags + `--output json`.
- `pkg/cmd/ci/dispatch.go` — pure-flag command with repeatable `key=value` input.
- `pkg/cmd/ci/run.go` (`NewCmdList` on line 706) — list command with filtering and `--output`.

A minimal command declaration looks like:

```go
cmd := &cobra.Command{
    Use:     "cancel <run-id>",
    Short:   "Cancel a CI workflow or job [beta]",
    Long:    `Cancel a queued or running CI workflow (and all its child jobs), or a single job within a workflow.`,
    Example: `  # Cancel a workflow (and all its jobs)
  depot ci cancel run_abc123 --workflow wf_xyz

  # Cancel a single job
  depot ci cancel run_abc123 --job job_xyz`,
    Args: cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error { /* ... */ },
}

cmd.Flags().StringVar(&orgID,      "org",    "", "Organization ID (required when user is a member of multiple organizations)")
cmd.Flags().StringVar(&token,      "token",  "", "Depot API token")
cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID to cancel")
cmd.Flags().StringVar(&jobID,      "job",    "", "Job ID to cancel within its workflow")
cmd.Flags().StringVar(&output,     "output", "", "Output format (json)")
```

---

## 2. Command declaration

### `Use:`
Format: `<verb> [<positional>] [flags]`. The `[flags]` tail is implicit in Cobra
— don't write it unless the command is a pass-through wrapper
(`pkg/cmd/exec/exec.go:163`, `pkg/cmd/sandbox/exec.go:16`).

Positional argument notation: angle brackets for required, square brackets for
optional.

| Pattern | Meaning | Example |
|---|---|---|
| `<name>` | Required positional | `status <run-id>` |
| `[name]` | Optional positional | `switch [org-id]` |
| `<name> [<name>...]` | One or more | `image rm <tag> [<tag>...]` |

**Drift to avoid in new commands:** `SECRET_NAME` / `VAR_NAME` (bare ALL_CAPS)
in `pkg/cmd/ci/secrets.go` and `pkg/cmd/ci/vars.go` predate the
`<angle-bracket>` convention. Prefer `<secret-name>` going forward. Existing
commands stay as-is.

### `Short:`
- Imperative mood.
- Capitalized first word.
- **No terminating period.**
- Append `[beta]` for unstable commands under `depot ci ...` (dropped once the
  surface is GA). Only the `ci` tree uses this today.

Good:
```go
Short: "Retry a failed CI job, or all failed jobs in a workflow [beta]",
Short: "List CI runs",
Short: "Authenticate the Depot CLI",
```

### `Long:`
- One or more sentence-form paragraphs separated by a blank line.
- Full sentences, end with a period.
- Describes behavior, inputs the server expects, and gotchas — **not** a flag
  reference (Cobra already prints those).

Drift: some commands omit `Long:` entirely (e.g. `pkg/cmd/org/show.go`,
`pkg/cmd/version/version.go`). Fine for one-line operations; add `Long:` the
moment there's behavior worth explaining (validation, server-side preconditions,
idempotency notes).

### `Example:`
- Raw string literal (backticks), **two-space indent**.
- Each example preceded by a `# comment` describing the intent.
- Separate examples with a blank line.

```go
Example: `  # Retry a single failed job
  depot ci retry run_abc123 --job job_xyz

  # Retry every failed job in a workflow
  depot ci retry run_abc123 --failed`,
```

### `Args:`
Use the Cobra constants, not custom validators, unless you need custom messaging.

| Constant | When |
|---|---|
| `cobra.NoArgs` | Pure-flag commands (`ci dispatch`, `ci run list`). |
| `cobra.ExactArgs(1)` | Single-resource commands (`ci status`, `ci cancel`). |
| `cobra.MinimumNArgs(1)` | Bulk commands (`image rm`, `ci secrets remove`). |
| `cobra.MaximumNArgs(1)` | Commands where the positional is optional (`org switch`, `projects create`). |

---

## 3. Flag naming and help text

### Naming

- **kebab-case, lowercase.** No exceptions. (`--ssh-after-step`, not
  `--sshAfterStep` or `--ssh_after_step`.)
- **One idea per flag.** Don't overload a single flag with modes; split
  (`--failed` vs `--job` on `ci retry`).
- **Positional for the primary object, flag for narrowing.** The thing being
  acted on (a run id, a secret name) is positional; the disambiguators are
  flags (`--workflow`, `--job`).

### Short flags

Used sparingly and only when the flag is typed often:

| Short | Long | Where |
|---|---|---|
| `-o` | `--output` | `pkg/cmd/ci/run.go:816`, `pkg/cmd/ci/ssh.go:92` |
| `-t` | `--tag` | `pkg/cmd/push/push.go:99`, `pkg/cmd/pull/pull.go:146` |

**Drift:** `--output` has `-o` on some CI commands and not others. Recommend
defaulting to `-o` for all `--output` flags in new code for parity.

### Help text

- **Start with a capital letter.**
- **No terminating period.**
- Parenthetical hints at the end for: format `(json)`, repeatability
  `(repeatable)`, requirement `(required)`, or when-required
  `(required when ...)`.

Patterns in use today:

```go
"Organization ID (required when user is a member of multiple organizations)"
"Workflow file basename, e.g. deploy.yml — NOT the full path (required)"
"Workflow input as key=value; repeat for multiple inputs"
"Filter by status (repeatable: queued, running, finished, failed, cancelled)"
"Output format (json)"
```

### Defaults

Use zero values for truly optional flags. Use sensible defaults for flags that
should never be empty, with the default reflected in the help:

```go
cmd.Flags().StringVar(&progress, "progress", "auto",
    `Set type of progress output ("auto", "plain", "tty", "quiet")`)
```

---

## 4. The `--output` convention

Every read-side command (anything that prints data the user might pipe) takes
a `--output` flag:

- `StringVar(&output, "output", "", "Output format (json)")` for JSON-only.
- Add `csv` to the hint when the command supports it
  (`pkg/cmd/org/list.go:89`: `"Non-interactive output format (json, csv)"`).
- Default is empty → TTY output (tables, progress, colors). Never print
  machine-readable output by default.
- Route through the same helper pattern as existing commands:

```go
if output == "json" {
    enc := json.NewEncoder(os.Stdout)
    enc.SetIndent("", "  ")
    return enc.Encode(resp)
}
```

Write-side commands (`dispatch`, `cancel`, `rerun`, `retry`) also accept
`--output json` and emit the raw RPC response when set. This gives scripts a
stable payload to parse without scraping TTY output.

**Drift:** help text varies — some say `Output format (json)`, others say
`Non-interactive output format (json, csv)`. Going forward, prefer
`Output format (json)` (or `(json, csv)`), and add `-o` as a short alias.

---

## 5. Auth flags: `--org` and `--token`

Every organization-scoped command exposes the same two flags and resolves them
through `ResolveOrgAuth`:

```go
cmd.Flags().StringVar(&orgID, "org",   "", "Organization ID (required when user is a member of multiple organizations)")
cmd.Flags().StringVar(&token, "token", "", "Depot API token")
```

- **Help text is boilerplate — keep it byte-identical** across commands so
  `--help` reads the same wherever you are in the tree. A handful of commands
  have drifted to `"Organization ID"` (`pkg/cmd/ci/vars.go:112`,
  `pkg/cmd/cargo/cargo.go:116`) or `"Depot token"` without the word "API"
  (`pkg/cmd/push/push.go:97`, `pkg/cmd/pull/pull.go:144`,
  `pkg/cmd/init/init.go:71`, `pkg/cmd/exec/exec.go:185`,
  `pkg/cmd/cache/reset.go:59`). Prefer the long form above for new code.
- Neither flag is marked required with Cobra; the helper decides at runtime so
  that env vars and global config can also supply the values.

---

## 6. Required and mutually exclusive flags

The CLI **does not** use Cobra's `MarkFlagRequired` or
`MarkFlagsMutuallyExclusive` anywhere today. Required/mutex checks are
hand-rolled inside `RunE`:

```go
if ssh && sshAfterStep > 0 {
    return fmt.Errorf("--ssh and --ssh-after-step are mutually exclusive")
}
```

**Recommendation for new code:** use the Cobra built-ins. They give us
consistent error wording, shell-completion awareness, and cleaner help output.

```go
cmd.MarkFlagsMutuallyExclusive("job", "failed")
cmd.MarkFlagsOneRequired("job", "failed")
cmd.MarkFlagRequired("repo")
```

Keep the fallback hand-rolled check only when the constraint is conditional
(e.g. `--workflow` required only if the run has multiple workflows).

Error message style for hand-rolled checks:

- Name the flags with their `--` prefix.
- Imperative or declarative, no trailing period.
- Use `fmt.Errorf`, not `log`.

Good:
```go
return fmt.Errorf("--job and --failed are mutually exclusive")
return fmt.Errorf("one of --job or --failed is required")
return fmt.Errorf("--workflow is required when the run contains multiple workflows")
```

---

## 7. Beta marker

Unstable surfaces under `depot ci` suffix `[beta]` on `Short:`:

```go
Short: "Manage Depot CI [beta]",
Short: "Cancel a CI workflow or job [beta]",
```

- Keep the marker on the parent group command **and** each subcommand until GA.
- Drop it in the same PR that flips the server contract to stable.
- Other command trees (`sandbox`, `claude`, `push`, `pull`) do not use
  `[beta]`; don't introduce it there without a separate discussion.

---

## 8. Positional args vs flags — the decision rule

- **Positional:** the single "what am I operating on" identifier. Examples:
  `<run-id>`, `<secret-name>`, `<tag>`.
- **Flag:** everything else — narrowing (`--workflow`, `--job`), filtering
  (`--status`, `--repo`), tuning (`--ssh-after-step`), auth (`--org`,
  `--token`), output control (`--output`).

If a command takes two "primary" identifiers (e.g. `workflow_id` **and**
`job_id` for `CancelJob`), keep the most specific one positional and lift the
parent to a flag. This is what `ci cancel <run-id> [--workflow | --job]` does:
the user already has the run id handy, and the CLI resolves the workflow from
the run for them via `resolveSingleWorkflow` / `findWorkflowForJob`
(`pkg/cmd/ci/resolve.go`).

---

## 9. Known drift (do not fix reactively)

These are flagged for awareness only. Each existing command keeps its current
surface until there is a separate reason to touch it.

| Drift | Dominant pattern | Stragglers |
|---|---|---|
| `<name>` vs `SECRET_NAME` in `Use:` | `<name>` | `ci secrets`, `ci vars` (pre-convention) |
| `-o` on `--output` | inconsistent | add `-o` in new code |
| `--org` help text | `Organization ID (required when user is a member of multiple organizations)` | `ci/vars.go`, `cargo/cargo.go` |
| `--token` help text | `Depot API token` | `push`, `pull`, `init`, `exec`, `cache reset` |
| `--output` help text | `Output format (json)` | list commands use `Non-interactive output format (json, csv)` |
| Required/mutex enforcement | hand-rolled `fmt.Errorf` in `RunE` | no Cobra built-ins used yet |

---

## 10. Quick checklist for a new verb

Before opening a PR for a new `depot ...` command, confirm:

- [ ] `Use:` uses `<angle>` for required and `[square]` for optional positionals.
- [ ] `Short:` is imperative, capitalized, no terminating period, `[beta]` if applicable.
- [ ] `Long:` explains behavior, not flags.
- [ ] `Example:` has at least two examples with `#` comments and blank-line separators.
- [ ] All flags are kebab-case, lowercase.
- [ ] Help text starts capital, no terminating period, parentheses for hints.
- [ ] `--org` and `--token` are present on any org-scoped command, with the canonical help strings.
- [ ] `--output` is present on anything printing data; accepts `json` minimum; defaults to TTY.
- [ ] Required / mutex flags use Cobra's built-ins (or hand-rolled with the
      error-message style above, if the constraint is conditional).
- [ ] Positional arg is the primary resource id; narrowing is a flag.
- [ ] Tests cover flag parsing, mutual exclusion, and `--output json` shape
      (see `pkg/cmd/ci/cancel_test.go` for a template).

---

## Appendix: greppable inventory

For anyone doing their own sweep:

```bash
# All flag declarations:
rg -n 'cmd\.Flags\(\)\.(String|Bool|Int|Duration|StringSlice|StringArray)Var[P]?\(' pkg/cmd

# All positional arg policies:
rg -n 'cobra\.(ExactArgs|MinimumNArgs|MaximumNArgs|NoArgs|RangeArgs|ArbitraryArgs)' pkg/cmd

# All --output usages:
rg -n '"output"' pkg/cmd

# Required / mutex (currently always hand-rolled):
rg -n 'mutually exclusive|is required' pkg/cmd
```
