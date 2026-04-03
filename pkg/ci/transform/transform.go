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

	// 4. Rewrite .github/ path references to .depot/
	pathChanges := transformGitHubPaths(root, migratedWorkflows)
	changes = append(changes, pathChanges...)

	// 5. Marshal the node tree back to bytes
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("failed to marshal YAML: %w", err)
	}
	enc.Close()

	output := buf.Bytes()

	// 6. Post-process: comment out disabled jobs in text
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

	// 7. Prepend header comment
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

// transformGitHubPaths walks all scalar nodes and rewrites local .github/ references to .depot/.
// Remote references like org/repo/.github/workflows/reusable.yml@ref are left untouched.
func transformGitHubPaths(node *yaml.Node, migratedWorkflows map[string]bool) []ChangeRecord {
	rewrote := false
	walkScalars(node, func(n *yaml.Node) {
		rewritten, changed := rewriteGitHubPaths(n.Value, migratedWorkflows)
		if changed {
			n.Value = rewritten
			rewrote = true
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
	// githubPathRe matches .github/actions or .github/workflows references.
	githubPathRe = regexp.MustCompile(`\.github/(actions|workflows)`)

	// remoteRefRe matches owner/repo/.github/(actions|workflows) patterns.
	// The char class [a-zA-Z0-9_.-] naturally excludes expression characters ($, {, }),
	// so expression-expanded paths like "${{ workspace }}/.github/" won't match.
	remoteRefRe = regexp.MustCompile(`[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+/\.github/(?:actions|workflows)`)

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
		subdir := s[m[2]:m[3]]

		if !shouldRewrite(s, start, end, subdir, migratedWorkflows, remoteSpans) {
			continue
		}

		b.WriteString(s[last:start])
		b.WriteString(".depot/" + subdir)
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
// Prevents matching inside longer names like "myapp.github/actions".
const boundaryChars = "/ \t\n\"'();|&="

// shouldRewrite decides whether a .github/(actions|workflows) match at [start:end]
// should be replaced with .depot/.
func shouldRewrite(s string, start, end int, subdir string, migratedWorkflows map[string]bool, remoteSpans [][]int) bool {
	if start > 0 && !strings.ContainsRune(boundaryChars, rune(s[start-1])) {
		return false
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
		if end >= len(s) || s[end] != '/' {
			return false
		}
		tail := pathTailRe.FindString(s[end+1:])
		if tail == "" || !migratedWorkflows[tail] {
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
	}
	return false
}

// walkScalars recursively visits all scalar nodes in a YAML tree.
func walkScalars(node *yaml.Node, fn func(*yaml.Node)) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode {
		fn(node)
		return
	}
	for _, child := range node.Content {
		walkScalars(child, fn)
	}
}

// RewriteGitHubPathsInDir walks a directory and rewrites .github/ → .depot/ references
// in all text files. Binary files are detected and skipped. Original file permissions
// are preserved. This is used for copied action files that aren't processed through
// the full YAML transform pipeline.
func RewriteGitHubPathsInDir(dir string, migratedWorkflows map[string]bool) (int, error) {
	rewritten := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", path, err)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		if isBinary(raw) {
			return nil
		}

		result, changed := rewriteGitHubPaths(string(raw), migratedWorkflows)
		if !changed {
			return nil
		}

		if err := os.WriteFile(path, []byte(result), info.Mode().Perm()); err != nil {
			return fmt.Errorf("failed to write %s: %w", path, err)
		}
		rewritten++
		return nil
	})
	return rewritten, err
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
