package transform

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"gopkg.in/yaml.v3"
)

func isBinary(data []byte) bool {
	contentType := http.DetectContentType(data)
	return !strings.HasPrefix(contentType, "text/")
}

// ChangeType categorizes a transformation change.
type ChangeType int

const (
	ChangeRunsOn         ChangeType = iota // runs-on label was remapped
	ChangeTriggerRemoved                   // Unsupported trigger was removed
	ChangeJobDisabled                      // Entire job was commented out
	ChangePathRewritten                    // .github/ path was rewritten to .depot/
	ChangeSparseCheckout                   // .depot/ paths added alongside .github/ in a checkout sparse-checkout
)

// ChangeRecord describes a single change made during transformation.
type ChangeRecord struct {
	Type    ChangeType
	JobName string // empty for trigger-level changes
	Detail  string
}

// TransformResult is the output of TransformWorkflow.
type TransformResult struct {
	Content     []byte         // Transformed YAML content
	Changes     []ChangeRecord // What was changed
	HasCritical bool           // Whether any jobs were disabled
}

// TransformWorkflow applies Depot CI migration transformations to a workflow.
// It uses the parsed WorkflowFile for structural info and the CompatibilityReport
// to identify issues, then transforms the raw YAML bytes.
// migratedWorkflows is a set of workflow relative paths (e.g., "ci.yml") that were
// selected for migration. When non-nil, only references to these workflows are rewritten.
// When nil, all .github/workflows/ references are rewritten. Actions are always rewritten.
func TransformWorkflow(raw []byte, wf *migrate.WorkflowFile, report *compat.CompatibilityReport, migratedWorkflows map[string]bool) (*TransformResult, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("unexpected YAML structure")
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root, got %d", root.Kind)
	}

	var changes []ChangeRecord

	// 1. Transform triggers
	triggerChanges := transformTriggers(root)
	changes = append(changes, triggerChanges...)

	// 2. Identify jobs that need to be disabled (uncorrectable issues)
	disabledJobs := findDisabledJobs(wf, report)

	// 3. Transform runs-on labels (skip disabled jobs)
	runsOnChanges := transformRunsOn(root, disabledJobs)
	changes = append(changes, runsOnChanges...)

	// 4. Handle actions/checkout sparse-checkout inputs before the generic path
	//    pass. These keep their .github/ entries and gain .depot/ siblings, so the
	//    generic pass must skip them (see transformSparseCheckout).
	sparseSkip, sparseChanges := transformSparseCheckout(root, migratedWorkflows)
	changes = append(changes, sparseChanges...)

	// 5. Rewrite .github/ path references to .depot/
	pathChanges := transformGitHubPaths(root, migratedWorkflows, sparseSkip)
	changes = append(changes, pathChanges...)

	// 6. Strip trailing whitespace from POSIX-shell step `run:` block scalars so yaml.v3
	//    keeps literal/folded style (e.g. `run: |`) instead of flattening to a quoted
	//    string. Non-shell inputs and non-POSIX shells are left byte-exact.
	sanitizeRunBlockScalars(root)

	// 7. Marshal the node tree back to bytes
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("failed to marshal YAML: %w", err)
	}
	enc.Close()

	output := buf.Bytes()

	// 8. Post-process: comment out disabled jobs in text
	if len(disabledJobs) > 0 {
		var disableChanges []ChangeRecord
		output, disableChanges = commentOutDisabledJobs(output, disabledJobs)
		changes = append(changes, disableChanges...)
	}

	hasCritical := false
	for _, c := range changes {
		if c.Type == ChangeJobDisabled {
			hasCritical = true
			break
		}
	}

	// 9. Prepend header comment
	header := buildHeaderComment(wf, changes)
	output = append([]byte(header), output...)

	return &TransformResult{
		Content:     output,
		Changes:     changes,
		HasCritical: hasCritical,
	}, nil
}

// transformTriggers removes unsupported triggers from the on: block.
func transformTriggers(root *yaml.Node) []ChangeRecord {
	var changes []ChangeRecord

	onKey, onVal := findMappingKey(root, "on")
	if onKey == nil && onVal == nil {
		// yaml.v3 may decode bare `on` as boolean true
		onKey, onVal = findMappingKey(root, "true")
	}
	if onKey == nil || onVal == nil {
		return nil
	}

	switch onVal.Kind {
	case yaml.ScalarNode:
		trigger := onVal.Value
		rule, ok := compat.TriggerRules[trigger]
		if ok && rule.Supported == compat.Unsupported {
			comment := fmt.Sprintf("Removed unsupported trigger: %s. %s", trigger, rule.Note)
			onKey.HeadComment = appendComment(onKey.HeadComment, comment)
			onVal.Kind = yaml.MappingNode
			onVal.Tag = "!!map"
			onVal.Value = ""
			onVal.Content = nil
			changes = append(changes, ChangeRecord{
				Type:   ChangeTriggerRemoved,
				Detail: fmt.Sprintf("Removed unsupported trigger %q", trigger),
			})
		}

	case yaml.SequenceNode:
		var removed []string
		var kept []*yaml.Node
		for _, item := range onVal.Content {
			if item.Kind != yaml.ScalarNode {
				kept = append(kept, item)
				continue
			}
			rule, ok := compat.TriggerRules[item.Value]
			if ok && rule.Supported == compat.Unsupported {
				removed = append(removed, item.Value)
			} else {
				kept = append(kept, item)
			}
		}
		if len(removed) > 0 {
			onVal.Content = kept
			for _, trigger := range removed {
				rule := compat.TriggerRules[trigger]
				comment := fmt.Sprintf("Removed unsupported trigger: %s. %s", trigger, rule.Note)
				onKey.HeadComment = appendComment(onKey.HeadComment, comment)
				changes = append(changes, ChangeRecord{
					Type:   ChangeTriggerRemoved,
					Detail: fmt.Sprintf("Removed unsupported trigger %q", trigger),
				})
			}
			if len(kept) == 0 {
				onVal.Kind = yaml.MappingNode
				onVal.Tag = "!!map"
				onVal.Content = nil
			}
		}

	case yaml.MappingNode:
		var removed []string
		var kept []*yaml.Node
		for i := 0; i < len(onVal.Content)-1; i += 2 {
			key := onVal.Content[i]
			val := onVal.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				kept = append(kept, key, val)
				continue
			}
			rule, ok := compat.TriggerRules[key.Value]
			if ok && rule.Supported == compat.Unsupported {
				removed = append(removed, key.Value)
			} else {
				kept = append(kept, key, val)
			}
		}
		if len(removed) > 0 {
			onVal.Content = kept
			for _, trigger := range removed {
				rule := compat.TriggerRules[trigger]
				comment := fmt.Sprintf("Removed unsupported trigger: %s. %s", trigger, rule.Note)
				onKey.HeadComment = appendComment(onKey.HeadComment, comment)
				changes = append(changes, ChangeRecord{
					Type:   ChangeTriggerRemoved,
					Detail: fmt.Sprintf("Removed unsupported trigger %q", trigger),
				})
			}
		}
	}

	return changes
}

// disabledJobInfo holds the reason a job should be commented out.
type disabledJobInfo struct {
	Reason string
}

// findDisabledJobs identifies jobs with uncorrectable (Unsupported) issues.
func findDisabledJobs(wf *migrate.WorkflowFile, report *compat.CompatibilityReport) map[string]disabledJobInfo {
	if report == nil {
		return nil
	}

	disabled := make(map[string]disabledJobInfo)
	for _, issue := range report.Issues {
		if issue.Level != compat.Unsupported {
			continue
		}
		// Match issue to a job by checking if the message references a job name
		for _, job := range wf.Jobs {
			if strings.Contains(issue.Message, fmt.Sprintf("Job %q", job.Name)) {
				if _, exists := disabled[job.Name]; !exists {
					disabled[job.Name] = disabledJobInfo{
						Reason: issue.Message,
					}
				}
			}
		}
	}

	return disabled
}

// transformRunsOn transforms runs-on labels in all jobs.
func transformRunsOn(root *yaml.Node, disabledJobs map[string]disabledJobInfo) []ChangeRecord {
	var changes []ChangeRecord

	_, jobsVal := findMappingKey(root, "jobs")
	if jobsVal == nil || jobsVal.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i < len(jobsVal.Content)-1; i += 2 {
		jobKey := jobsVal.Content[i]
		jobVal := jobsVal.Content[i+1]

		if jobKey.Kind != yaml.ScalarNode {
			continue
		}
		jobName := jobKey.Value

		// Skip disabled jobs — they'll be commented out entirely
		if _, disabled := disabledJobs[jobName]; disabled {
			continue
		}

		if jobVal.Kind != yaml.MappingNode {
			continue
		}

		_, runsOnVal := findMappingKey(jobVal, "runs-on")
		if runsOnVal == nil {
			continue
		}

		jobChanges := transformRunsOnNode(runsOnVal, jobName)
		changes = append(changes, jobChanges...)
	}

	return changes
}

// transformRunsOnNode transforms a single runs-on node value.
func transformRunsOnNode(node *yaml.Node, jobName string) []ChangeRecord {
	var changes []ChangeRecord

	switch node.Kind {
	case yaml.ScalarNode:
		original := node.Value
		newLabel, changed, reason := migrate.MapLabel(original)
		if changed {
			node.Value = newLabel
			node.LineComment = fmt.Sprintf("was: %s. %s", original, reason)
			changes = append(changes, ChangeRecord{
				Type:    ChangeRunsOn,
				JobName: jobName,
				Detail:  fmt.Sprintf("Changed runs-on from %q to %q in job %q", original, newLabel, jobName),
			})
		}

	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			original := item.Value
			newLabel, changed, reason := migrate.MapLabel(original)
			if changed {
				item.Value = newLabel
				item.LineComment = fmt.Sprintf("was: %s. %s", original, reason)
				changes = append(changes, ChangeRecord{
					Type:    ChangeRunsOn,
					JobName: jobName,
					Detail:  fmt.Sprintf("Changed runs-on from %q to %q in job %q", original, newLabel, jobName),
				})
			}
		}
	}

	return changes
}

// transformGitHubPaths walks all nodes and rewrites local .github/ references to .depot/
// in both scalar values and YAML comments (HeadComment, LineComment, FootComment).
// Remote references like org/repo/.github/workflows/reusable.yml@ref are left untouched.
// Nodes in skip are left entirely alone; sparse-checkout values handled by
// transformSparseCheckout live there so their .github/ entries are preserved.
func transformGitHubPaths(node *yaml.Node, migratedWorkflows map[string]bool, skip map[*yaml.Node]bool) []ChangeRecord {
	rewrote := false
	rewrite := func(s string) string {
		result, changed := rewriteGitHubPaths(s, migratedWorkflows)
		if changed {
			rewrote = true
		}
		return result
	}
	walkNodes(node, func(n *yaml.Node) {
		if skip[n] {
			return
		}
		if n.Kind == yaml.ScalarNode {
			n.Value = rewrite(n.Value)
		}
		if n.HeadComment != "" {
			n.HeadComment = rewrite(n.HeadComment)
		}
		if n.LineComment != "" {
			n.LineComment = rewrite(n.LineComment)
		}
		if n.FootComment != "" {
			n.FootComment = rewrite(n.FootComment)
		}
	})
	if !rewrote {
		return nil
	}
	return []ChangeRecord{{
		Type:   ChangePathRewritten,
		Detail: "Rewrote .github/ path references to .depot/",
	}}
}

var (
	// githubPathRe matches .github/actions or .github/workflows references. The
	// separator group tolerates a backslash-escaped slash (\/) so paths embedded in
	// regexes or escaped string literals inside action sources — e.g. \.github\/actions
	// — are matched the same as plain paths in fixtures. The captured separator is
	// reused in the replacement so the escaping style is preserved.
	githubPathRe = regexp.MustCompile(`\.github(\\?/)(actions|workflows)`)

	// remoteRefRe matches owner/repo/.github/(actions|workflows) patterns.
	// The char class [a-zA-Z0-9_.-] naturally excludes expression characters ($, {, }),
	// so expression-expanded paths like "${{ workspace }}/.github/" won't match.
	// Each separator, and the dot before "github", tolerates a backslash-escape (\/
	// and \.) so escaped remote refs embedded in code strings — e.g.
	// org\/repo\/\.github\/actions — are still classified as remote and left alone,
	// matching how githubPathRe treats a preceding backslash as a boundary.
	remoteRefRe = regexp.MustCompile(`[a-zA-Z0-9_.-]+\\?/[a-zA-Z0-9_.-]+\\?/\\?\.github\\?/(?:actions|workflows)`)

	// pathTailRe captures a file path segment after a / delimiter, stopping at
	// whitespace or shell metacharacters.
	pathTailRe = regexp.MustCompile(`^[^\s"'();|&=]+`)
)

// rewriteGitHubPaths replaces local .github/{actions,workflows} references with
// .depot/ equivalents in a single pass. Remote repo references (org/repo/.github/...),
// URLs, and non-migrated .github/ files (dependabot.yml, CODEOWNERS, etc.) are preserved.
//
// migratedWorkflows controls workflow filtering: when non-nil, only references to
// workflows in the set are rewritten. When nil, all workflow references are rewritten.
// Actions are always rewritten (the entire .github/actions/ directory is copied).
func rewriteGitHubPaths(s string, migratedWorkflows map[string]bool) (string, bool) {
	candidates := githubPathRe.FindAllStringSubmatchIndex(s, -1)
	if len(candidates) == 0 {
		return s, false
	}

	remoteSpans := remoteRefRe.FindAllStringIndex(s, -1)

	var b strings.Builder
	last := 0
	changed := false

	for _, m := range candidates {
		start, end := m[0], m[1]
		sep := s[m[2]:m[3]]
		subdir := s[m[4]:m[5]]

		if !shouldRewrite(s, start, end, subdir, migratedWorkflows, remoteSpans) {
			continue
		}

		b.WriteString(s[last:start])
		b.WriteString(".depot" + sep + subdir)
		last = end
		changed = true
	}

	if !changed {
		return s, false
	}
	b.WriteString(s[last:])
	return b.String(), true
}

// boundaryChars are characters that can validly precede ".github/" as a path reference.
// Prevents matching inside longer names like "myapp.github/actions". The backslash is
// included so regex/escaped-string forms in action sources (\.github/actions) are
// treated as references, matching how the same paths in fixtures are rewritten.
const boundaryChars = "/ \t\n\"'();|&=\\"

// shouldRewrite decides whether a .github/(actions|workflows) match at [start:end]
// should be replaced with .depot/.
func shouldRewrite(s string, start, end int, subdir string, migratedWorkflows map[string]bool, remoteSpans [][]int) bool {
	if start > 0 {
		prev := s[start-1]
		if prev == '\\' {
			// An escaped dot (\.github) is only a path boundary when the backslash
			// itself sits at a boundary. If a path character precedes the backslash —
			// e.g. a regex like `myapp\.github/actions` — this is a longer token, not a
			// path reference, and must be skipped just as the unescaped form
			// (myapp.github/actions) is.
			if start >= 2 && !strings.ContainsRune(boundaryChars, rune(s[start-2])) {
				return false
			}
		} else if !strings.ContainsRune(boundaryChars, rune(prev)) {
			return false
		}
	}

	// Must not continue into a longer dir name (e.g., ".github/actions-custom")
	if end < len(s) && isPathChar(s[end]) {
		return false
	}

	// Skip matches inside URLs. This is a procedural backward scan rather than a
	// regex pre-pass because URL patterns overlap heavily with the paths we want to
	// match, and a backward scan for "://" is simpler than managing overlapping spans.
	if isURL(s, start) {
		return false
	}

	// Skip remote repo refs (owner/repo/.github/...) detected by remoteRefRe.
	// Deep filesystem paths (/home/.../repo/.github/) are distinguished by checking
	// whether the match is preceded by /, which indicates a deeper path, not a remote ref.
	for _, span := range remoteSpans {
		if start >= span[0] && start < span[1] {
			if span[0] == 0 || s[span[0]-1] != '/' {
				return false
			}
		}
	}

	// Partial migration: for workflows, only rewrite references to selected files.
	// Bare directory refs (e.g., "ls .github/workflows") are skipped when filtering
	// is active since the directory still partially lives at .github/.
	if subdir == "workflows" && migratedWorkflows != nil {
		// The separator after "workflows" may itself be escaped (\/) when the path
		// is embedded in a regex or escaped string, so consume either form before
		// reading the filename tail.
		rest := s[end:]
		switch {
		case strings.HasPrefix(rest, "/"):
			rest = rest[1:]
		case strings.HasPrefix(rest, "\\/"):
			rest = rest[2:]
		default:
			return false
		}
		tail := pathTailRe.FindString(rest)
		if tail == "" {
			return false
		}
		// The tail itself may carry escaped separators (scripts\/build.sh) when the
		// reference is embedded in a regex or escaped string. The allow-list is keyed
		// by plain slash paths (scripts/build.sh), so unescape before the lookup —
		// otherwise an escaped nested-sibling reference would be left at .github/.
		if !migratedWorkflows[strings.ReplaceAll(tail, "\\/", "/")] {
			return false
		}
	}

	return true
}

// isPathChar returns true for characters that can continue a directory name,
// distinguishing ".github/actions/..." from ".github/actions-custom/...".
func isPathChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.'
}

// isURL scans backward from idx to check if the .github/ match appears inside a
// URL (contains "://" before the match within the same token). This is procedural
// rather than a regex pre-pass because URL patterns overlap with the paths we're
// rewriting, and a backward scan for "://" is simpler than managing span overlaps.
func isURL(s string, idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '"' || c == '\'' ||
			c == ';' || c == '|' || c == '&' || c == '(' || c == ')' || c == '=' {
			return false
		}
		if i >= 2 && s[i-2:i+1] == "://" {
			return true
		}
		// Also recognize a backslash-escaped protocol separator (":\/"), which is how
		// a URL appears when embedded in an escaped string literal or regex — e.g.
		// "https:\/\/github.com\/org\/repo\/.github\/actions". Without this, the
		// escaped-slash matching would rewrite .github inside such URLs.
		if i >= 2 && s[i-2:i+1] == ":\\/" {
			return true
		}
	}
	return false
}

// walkNodes recursively visits all nodes in a YAML tree.
func walkNodes(node *yaml.Node, fn func(*yaml.Node)) {
	if node == nil {
		return
	}
	fn(node)
	for _, child := range node.Content {
		walkNodes(child, fn)
	}
}

// sanitizeRunBlockScalars strips trailing whitespace from the lines of shell-step `run:`
// block scalars. yaml.v3's emitter refuses block style for any scalar with a line ending
// in a space or tab and silently falls back to a double-quoted, single-line string —
// flattening a readable `run: |` block into "\n"-escaped text. In a POSIX shell that
// trailing whitespace is inert (modulo the quote and continuation cases
// trimTrailingWhitespace guards), so trimming it restores the block style, and with it
// review legibility, without changing what runs.
//
// It is deliberately narrow on two axes so it never touches data:
//   - Only genuine step `run:` fields under jobs.<job>.steps[] are considered. A block
//     scalar passed to an action input that happens to be named `run` (under `with:`)
//     is not a shell command and is left byte-exact, as are all other block scalars
//     (e.g. `with: body: |`, whose trailing spaces may be Markdown hard breaks).
//   - Only steps whose effective shell is POSIX (bash/sh, including the unset default on
//     the Linux runners Depot CI targets) are trimmed. Under cmd, PowerShell, python,
//     and the like trailing spaces can be significant (e.g. cmd `set NAME=value   `), so
//     those run blocks are left byte-exact and yaml.v3 keeps them via its quoted fallback.
func sanitizeRunBlockScalars(root *yaml.Node) {
	_, jobsVal := findMappingKey(root, "jobs")
	if jobsVal == nil || jobsVal.Kind != yaml.MappingNode {
		return
	}

	workflowShell := defaultRunShell(root)

	for i := 1; i < len(jobsVal.Content); i += 2 {
		job := jobsVal.Content[i]
		if job.Kind != yaml.MappingNode {
			continue
		}

		jobShell := defaultRunShell(job)
		if jobShell == "" {
			jobShell = workflowShell
		}

		_, steps := findMappingKey(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			if step.Kind != yaml.MappingNode {
				continue
			}
			_, runVal := findMappingKey(step, "run")
			if runVal == nil || runVal.Kind != yaml.ScalarNode {
				continue
			}
			if runVal.Style != yaml.LiteralStyle && runVal.Style != yaml.FoldedStyle {
				continue
			}

			shell := ""
			if _, shellVal := findMappingKey(step, "shell"); shellVal != nil && shellVal.Kind == yaml.ScalarNode {
				shell = shellVal.Value
			}
			if shell == "" {
				shell = jobShell
			}
			if !isPOSIXShell(shell) {
				continue
			}

			trimTrailingWhitespace(runVal)
		}
	}
}

// defaultRunShell returns the defaults.run.shell value declared on a workflow- or
// job-level mapping, or "" when unset.
func defaultRunShell(m *yaml.Node) string {
	_, defaults := findMappingKey(m, "defaults")
	if defaults == nil || defaults.Kind != yaml.MappingNode {
		return ""
	}
	_, run := findMappingKey(defaults, "run")
	if run == nil || run.Kind != yaml.MappingNode {
		return ""
	}
	_, shell := findMappingKey(run, "shell")
	if shell == nil || shell.Kind != yaml.ScalarNode {
		return ""
	}
	return shell.Value
}

// isPOSIXShell reports whether a run step's effective shell is one where trailing
// whitespace in the command text is inert. An empty value is the GitHub Actions default,
// which is bash (falling back to sh) on the Linux and macOS runners Depot CI targets.
// Anything else — pwsh, cmd, python, or a custom template like "perl {0}" — may attach
// meaning to trailing whitespace, so those run blocks are left byte-exact.
func isPOSIXShell(shell string) bool {
	switch shell {
	case "", "bash", "sh":
		return true
	default:
		return false
	}
}

// trimTrailingWhitespace strips trailing spaces and tabs from each line of a block
// scalar's value, but only on lines where that whitespace is provably inert. It leaves
// bytes untouched in the three cases where trailing whitespace is load-bearing in shell:
// heredoc bodies, escaped line continuations, and text inside a quote that spans lines.
func trimTrailingWhitespace(n *yaml.Node) {
	// A heredoc payload can legitimately depend on trailing whitespace, and we can't
	// tell a heredoc body from ordinary commands at this layer, so leave any scalar
	// containing a heredoc operator untouched — yaml.v3's quoted fallback keeps it
	// byte-exact. Ordinary run blocks (the common case) have no heredoc and are still
	// tidied back into readable block style.
	if strings.Contains(n.Value, "<<") {
		return
	}
	lines := strings.Split(n.Value, "\n")
	inSingle, inDouble := false, false
	for i := range lines {
		// A line's trailing whitespace is only safe to trim when the line does not
		// end inside an open single- or double-quoted string. If a quote is still
		// open, those spaces are string data continuing onto the next line — e.g.
		// `printf '%s' 'foo  ` closing as `bar'` two lines down — and trimming them
		// would change the value the shell sees. Track quote state across lines and
		// skip any line that ends mid-quote; yaml.v3 then keeps that scalar quoted.
		openAtEnd := quoteStateAtLineEnd(lines[i], &inSingle, &inDouble)
		if openAtEnd {
			continue
		}
		trimmed := strings.TrimRight(lines[i], " \t")
		// Leave a line whose trimmed form ends in a line-continuation escape untouched.
		// Trimming "foo \   " (a literal escaped space) down to "foo \" would splice
		// this line onto the next, changing what the migrated workflow executes. The
		// continuation character is shell-specific — backslash in POSIX sh, backtick in
		// PowerShell, caret in cmd — so guard all three since a run step may set any of
		// them via `shell:`. Such a line keeps its trailing whitespace, so yaml.v3 still
		// quotes that one scalar; correctness wins over tidiness in this rare case.
		if n := len(trimmed); n > 0 {
			switch trimmed[n-1] {
			case '\\', '`', '^':
				continue
			}
		}
		lines[i] = trimmed
	}
	n.Value = strings.Join(lines, "\n")
}

// quoteStateAtLineEnd scans a single line of shell text, advancing the single- and
// double-quote state carried across lines, and reports whether a quote is still open
// at the end of the line. It models the quoting rules that matter here: single quotes
// are literal (no escapes, only another ' closes them); inside double quotes a backslash
// (POSIX sh) or backtick (PowerShell) escapes the next character; and a backslash outside
// quotes escapes the next character too. It is intentionally conservative — exotic forms
// like $'...' ANSI-C quoting are not modeled — but every simplification errs toward
// treating a quote as still open, which only ever leaves a scalar quoted (less tidy) and
// never trims whitespace that was actually inside a quote.
func quoteStateAtLineEnd(line string, inSingle, inDouble *bool) bool {
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case *inSingle:
			if c == '\'' {
				*inSingle = false
			}
		case *inDouble:
			switch c {
			case '\\', '`':
				i++ // \ (POSIX sh) or ` (PowerShell) escapes the next char inside double quotes
			case '"':
				*inDouble = false
			}
		default:
			switch c {
			case '\'':
				*inSingle = true
			case '"':
				*inDouble = true
			case '\\':
				i++ // escaped char outside quotes
			}
		}
	}
	return *inSingle || *inDouble
}

// isCheckoutAction reports whether a `uses` value refers to actions/checkout.
func isCheckoutAction(uses string) bool {
	name := uses
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	return name == "actions/checkout"
}

// transformSparseCheckout augments actions/checkout `sparse-checkout` inputs instead of
// letting the generic path pass rewrite them. Rewriting a sparse-checkout entry from
// .github/ to .depot/ stops git from materializing .github/, which breaks steps that
// still reference scripts living under .github/ — only .github/actions and the selected
// .github/workflows move to .depot/, so everything else legitimately stays. To keep both
// working, each .github/(actions|workflows) entry is preserved and a .depot/ sibling is
// added, making the checkout a superset. The returned set marks the handled value nodes
// (and their sequence items) so transformGitHubPaths leaves the .github/ entries alone.
func transformSparseCheckout(root *yaml.Node, migratedWorkflows map[string]bool) (map[*yaml.Node]bool, []ChangeRecord) {
	skip := make(map[*yaml.Node]bool)
	augmented := false

	_, jobsVal := findMappingKey(root, "jobs")
	if jobsVal == nil || jobsVal.Kind != yaml.MappingNode {
		return skip, nil
	}

	for i := 1; i < len(jobsVal.Content); i += 2 {
		job := jobsVal.Content[i]
		if job.Kind != yaml.MappingNode {
			continue
		}
		_, steps := findMappingKey(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			if step.Kind != yaml.MappingNode {
				continue
			}
			_, uses := findMappingKey(step, "uses")
			if uses == nil || uses.Kind != yaml.ScalarNode || !isCheckoutAction(uses.Value) {
				continue
			}
			_, with := findMappingKey(step, "with")
			if with == nil || with.Kind != yaml.MappingNode {
				continue
			}
			_, sparse := findMappingKey(with, "sparse-checkout")
			if sparse == nil {
				continue
			}
			// Always skip the sparse-checkout value so the generic pass never
			// rewrites its .github/ entries, whether or not we add siblings.
			skip[sparse] = true
			for _, item := range sparse.Content {
				skip[item] = true
			}
			if augmentSparseCheckout(sparse, migratedWorkflows) {
				augmented = true
			}
		}
	}

	if !augmented {
		return skip, nil
	}
	return skip, []ChangeRecord{{
		Type:   ChangeSparseCheckout,
		Detail: "Added .depot/ paths alongside .github/ in checkout sparse-checkout (kept .github/ so steps that still reference it keep working)",
	}}
}

// rewriteSparseEntry computes the .depot/ sibling for a single sparse-checkout entry,
// preserving a leading "!" negation. In non-cone mode sparse-checkout accepts
// gitignore-style patterns, so `!.github/actions/cache` excludes that path; mirroring it
// to `!.depot/actions/cache` keeps the migrated checkout from re-including files the
// original explicitly excluded.
//
// The .depot/ path is computed with the full (nil) filter so that even a bare
// ".github/workflows" directory entry gains its .depot/workflows counterpart — a
// partial filter would decline that (correctly, for a run: reference), but sparse-checkout
// must still materialize the .depot/ tree the migration produced. To avoid the reverse
// mistake — pointing sparse-checkout at a .depot/workflows/<file> that was never copied —
// a specific workflow-file entry is only mirrored when that file is actually in the
// partial allow-list (see sparseWorkflowCovered). Returns whether a sibling exists.
func rewriteSparseEntry(entry string, migratedWorkflows map[string]bool) (string, bool) {
	neg := ""
	body := entry
	if strings.HasPrefix(body, "!") {
		neg = "!"
		body = body[1:]
	}
	if migratedWorkflows != nil && !sparseWorkflowCovered(body, migratedWorkflows) {
		return entry, false
	}
	depot, ok := rewriteGitHubPaths(body, nil)
	if !ok {
		return entry, false
	}
	return neg + depot, true
}

// sparseWorkflowCovered reports whether a sparse-checkout entry's .depot/ counterpart
// will actually contain something after a partial migration. Actions always migrate, so
// any non-workflows entry maps. A bare ".github/workflows" directory maps because it
// holds the migrated workflows and their copied siblings. A glob under workflows
// (e.g. ".github/workflows/*.yml") maps because it is self-limiting — it includes only
// the migrated files that exist under .depot/workflows. A specific literal
// ".github/workflows/<tail>" maps only when <tail> is a migrated file or a directory
// prefix of one — otherwise .depot/workflows/<tail> would not exist and the mirrored
// pattern would match nothing.
func sparseWorkflowCovered(entry string, migratedWorkflows map[string]bool) bool {
	const wf = ".github/workflows"
	idx := strings.Index(entry, wf)
	if idx < 0 {
		return true // not a workflows entry (e.g. .github/actions) — always maps
	}
	rest := strings.TrimPrefix(entry[idx+len(wf):], "/")
	tail := strings.TrimSuffix(rest, "/")
	if tail == "" {
		return true // the bare .github/workflows directory
	}
	// A glob pattern (gitignore-style, as non-cone sparse-checkout accepts) is
	// self-limiting: mirroring ".github/workflows/*.yml" to ".depot/workflows/*.yml"
	// includes whatever migrated files landed under .depot/workflows and matches nothing
	// otherwise. Mirroring it is therefore always safe — and necessary, since the glob is
	// how the original checked those files out. Only a literal path asserts one specific
	// file, so only a literal tail is gated on actual coverage below.
	if strings.ContainsAny(tail, "*?[") {
		return true
	}
	for k := range migratedWorkflows {
		if k == tail || strings.HasPrefix(k, tail+"/") {
			return true
		}
	}
	return false
}

// augmentSparseCheckout adds a .depot/ sibling for each .github/(actions|workflows)
// entry in a sparse-checkout value, leaving the original entries in place. See
// rewriteSparseEntry for how the .depot/ path is derived under a partial migration.
// Returns whether anything was added.
//
// The mirror is order-faithful: each .depot/ sibling keeps the relative position of its
// .github/ source (inserted right after it in a block scalar, appended in source order in
// a sequence). Non-cone sparse-checkout applies gitignore-style rules where the last
// matching pattern wins, so preserving order is what makes the .depot/ patterns exclude
// and include in the same sequence the author intended for .github/ — e.g. a
// `.depot/actions` include followed by a `!.depot/actions/cache` exclude drops the cache
// exactly as the original did. Because .github/ and .depot/ patterns live under different
// top-level directories they never cross-match, so a .github/ line interleaved among the
// .depot/ lines cannot change which .depot/ paths materialize; the outcome depends only on
// the order among the .depot/ patterns, which equals the order among their .github/
// sources. Reordering the mirror (e.g. hoisting includes above excludes) would instead
// make the migrated checkout diverge from the source's own semantics.
func augmentSparseCheckout(node *yaml.Node, migratedWorkflows map[string]bool) bool {
	switch node.Kind {
	case yaml.ScalarNode:
		lines := strings.Split(node.Value, "\n")
		existing := make(map[string]bool, len(lines))
		for _, l := range lines {
			existing[strings.TrimSpace(l)] = true
		}
		var out []string
		changed := false
		for _, l := range lines {
			out = append(out, l)
			trimmed := strings.TrimSpace(l)
			if trimmed == "" {
				continue
			}
			depot, ok := rewriteSparseEntry(trimmed, migratedWorkflows)
			if !ok || depot == trimmed || existing[depot] {
				continue
			}
			indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			out = append(out, indent+depot)
			existing[depot] = true
			changed = true
		}
		if changed {
			node.Value = strings.Join(out, "\n")
		}
		return changed

	case yaml.SequenceNode:
		existing := make(map[string]bool, len(node.Content))
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				existing[item.Value] = true
			}
		}
		var additions []*yaml.Node
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			depot, ok := rewriteSparseEntry(item.Value, migratedWorkflows)
			if !ok || depot == item.Value || existing[depot] {
				continue
			}
			additions = append(additions, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: depot})
			existing[depot] = true
		}
		if len(additions) > 0 {
			node.Content = append(node.Content, additions...)
			return true
		}
		return false
	}
	return false
}

// RewriteGitHubPathsInDir walks a directory and rewrites .github/ → .depot/ references
// in all text files. Binary files and symlinks are skipped. Original file permissions
// are preserved. This is used for copied action files that aren't processed through
// the full YAML transform pipeline.
//
// migratedWorkflows controls workflow path filtering — pass nil to rewrite all
// .github/workflows/ references (full migration), or a set of relative filenames
// (e.g., {"ci.yml": true}) to only rewrite references to those specific workflows.
// Actions (.github/actions/) are always rewritten regardless of this parameter.
func RewriteGitHubPathsInDir(dir string, migratedWorkflows map[string]bool) (int, error) {
	rewritten := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		changed, err := RewriteGitHubPathsInFile(path, migratedWorkflows)
		if err != nil {
			return err
		}
		if changed {
			rewritten++
		}
		return nil
	})
	return rewritten, err
}

// RewriteGitHubPathsInFile rewrites .github/ → .depot/ references in a single text
// file, skipping symlinks and binary files and preserving the file's permissions.
// It reports whether the file was modified. Used for copied assets (action files and
// workflow sibling files) that don't pass through the YAML transform pipeline.
func RewriteGitHubPathsInFile(path string, migratedWorkflows map[string]bool) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("failed to stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}

	if isBinary(raw) {
		return false, nil
	}

	result, changed := rewriteGitHubPaths(string(raw), migratedWorkflows)
	if !changed {
		return false, nil
	}

	if err := os.WriteFile(path, []byte(result), info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("failed to write %s: %w", path, err)
	}
	return true, nil
}

// commentOutDisabledJobs does a text-level pass to comment out entire job blocks.
func commentOutDisabledJobs(content []byte, disabledJobs map[string]disabledJobInfo) ([]byte, []ChangeRecord) {
	if len(disabledJobs) == 0 {
		return content, nil
	}

	lines := strings.Split(string(content), "\n")
	var changes []ChangeRecord

	for jobName, info := range disabledJobs {
		lines, _ = commentOutJobBlock(lines, jobName, info.Reason)
		changes = append(changes, ChangeRecord{
			Type:    ChangeJobDisabled,
			JobName: jobName,
			Detail:  fmt.Sprintf("Disabled job %q: %s", jobName, info.Reason),
		})
	}

	return []byte(strings.Join(lines, "\n")), changes
}

// commentOutJobBlock finds a job key at indent level 2 (under jobs:) and comments out
// all lines belonging to that job.
func commentOutJobBlock(lines []string, jobName string, reason string) ([]string, bool) {
	jobPattern := "  " + jobName + ":"
	startIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == jobPattern || strings.HasPrefix(trimmed, jobPattern+" ") {
			startIdx = i
			break
		}
	}

	if startIdx < 0 {
		return lines, false
	}

	// Find end of this job block: next line at indent <= 2 spaces (sibling job or end of jobs)
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent <= 2 && !strings.HasPrefix(trimmed, "#") {
			endIdx = i
			break
		}
	}

	var result []string
	result = append(result, lines[:startIdx]...)
	result = append(result, fmt.Sprintf("  # DISABLED: %s", reason))

	for i := startIdx; i < endIdx; i++ {
		if lines[i] == "" {
			result = append(result, "")
		} else {
			result = append(result, "  # "+lines[i])
		}
	}

	result = append(result, lines[endIdx:]...)
	return result, true
}

// buildHeaderComment generates the header comment block for a transformed workflow.
func buildHeaderComment(wf *migrate.WorkflowFile, changes []ChangeRecord) string {
	var b strings.Builder
	b.WriteString("# Depot CI Migration\n")
	if wf != nil && wf.Path != "" {
		// Extract just the filename portion
		source := wf.Path
		if idx := strings.Index(source, ".github/workflows/"); idx >= 0 {
			source = source[idx:]
		}
		b.WriteString(fmt.Sprintf("# Source: %s\n", source))
	}
	b.WriteString("#\n")

	if len(changes) == 0 {
		b.WriteString("# No changes were necessary.\n")
	} else {
		b.WriteString("# Changes made:\n")
		for _, line := range summarizeChanges(changes) {
			b.WriteString(fmt.Sprintf("# - %s\n", line))
		}
	}
	b.WriteString("\n")

	return b.String()
}

// summarizeChanges condenses repeated changes into concise descriptions.
// e.g. multiple jobs with the same runs-on mapping become a single "throughout" line.
func summarizeChanges(changes []ChangeRecord) []string {
	// Group runs-on changes by (oldLabel, newLabel)
	type runsOnKey struct{ from, to string }
	runsOnCounts := make(map[runsOnKey]int)
	var runsOnOrder []runsOnKey

	var lines []string
	hasPathRewrite := false
	for _, c := range changes {
		switch c.Type {
		case ChangeRunsOn:
			from, to := parseRunsOnDetail(c.Detail)
			key := runsOnKey{from, to}
			if runsOnCounts[key] == 0 {
				runsOnOrder = append(runsOnOrder, key)
			}
			runsOnCounts[key]++
		case ChangePathRewritten:
			hasPathRewrite = true
		default:
			lines = append(lines, c.Detail)
		}
	}

	if hasPathRewrite {
		lines = append(lines, "Rewrote .github/ path references to .depot/")
	}

	// Check if all runs-on changes are standard GitHub → Depot mappings
	allStandard := true
	for _, key := range runsOnOrder {
		if _, ok := migrate.GitHubToDepotRunner[strings.ToLower(key.from)]; !ok {
			allStandard = false
			break
		}
	}

	if allStandard && len(runsOnOrder) > 0 {
		// All changes are standard mappings — single summary line
		lines = append(lines, "Changed GitHub runs-on labels to their Depot equivalents")
	} else {
		// Mix of standard and nonstandard — show per-mapping details
		for _, key := range runsOnOrder {
			count := runsOnCounts[key]
			if count == 1 {
				for _, c := range changes {
					if c.Type == ChangeRunsOn {
						from, to := parseRunsOnDetail(c.Detail)
						if from == key.from && to == key.to {
							lines = append(lines, c.Detail)
							break
						}
					}
				}
			} else {
				lines = append(lines, fmt.Sprintf("Changed runs-on from %q to %q throughout", key.from, key.to))
			}
		}
	}

	return lines
}

// parseRunsOnDetail extracts the from/to labels from a runs-on change detail string.
func parseRunsOnDetail(detail string) (from, to string) {
	// Format: `Changed runs-on from "X" to "Y" in job "Z"`
	const fromPrefix = "Changed runs-on from \""
	idx := strings.Index(detail, fromPrefix)
	if idx < 0 {
		return "", ""
	}
	rest := detail[idx+len(fromPrefix):]
	endFrom := strings.Index(rest, "\"")
	if endFrom < 0 {
		return "", ""
	}
	from = rest[:endFrom]
	rest = rest[endFrom+1:]

	const toPrefix = " to \""
	idx = strings.Index(rest, toPrefix)
	if idx < 0 {
		return from, ""
	}
	rest = rest[idx+len(toPrefix):]
	endTo := strings.Index(rest, "\"")
	if endTo < 0 {
		return from, ""
	}
	to = rest[:endTo]
	return from, to
}

// findMappingKey finds a key-value pair in a mapping node by key name.
func findMappingKey(mapping *yaml.Node, key string) (keyNode, valNode *yaml.Node) {
	if mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return k, v
		}
	}
	return nil, nil
}

// appendComment appends a line to an existing comment block.
func appendComment(existing, newLine string) string {
	if existing == "" {
		return newLine
	}
	return existing + "\n" + newLine
}
