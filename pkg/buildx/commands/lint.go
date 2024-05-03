package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/debuglog"
	"github.com/depot/cli/pkg/progresshelper"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/morikuni/aec"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	Hadolint = "hadolint/hadolint:2.12.0"
	Semgrep  = "returntocorp/semgrep:1.34.1"
)

// LintFailed is the error returned when linting fails.  Used to fail the build.
var LintFailed = errors.New("linting failed")

type LintFailure int

const (
	// LintError fails builds on all errors.
	LintError LintFailure = iota
	// LintWarn fails builds on all warnings and errors.
	LintWarn
	// LintInfo fails builds on all linting issues.
	LintInfo
	// LintNone does not fail the build on linting issues.
	LintNone
	// LintSkip skips linting.
	LintSkip
)

func NewLintFailureMode(lint bool, lintFailOn string) LintFailure {
	if !lint {
		return LintSkip
	}

	switch strings.ToLower(lintFailOn) {
	case "error":
		return LintError
	case "warn", "warning":
		return LintWarn
	case "info":
		return LintInfo
	case "none":
		return LintNone
	default:
		return LintError
	}
}

func (l LintFailure) Color() aec.ANSI {
	switch l {
	case LintError:
		return aec.RedF
	case LintWarn:
		return aec.YellowF
	case LintInfo:
		return aec.BlueF
	default:
		return aec.GreenF
	}
}

type Linter struct {
	FailureMode LintFailure
	Clients     []*client.Client
	BuildxNodes []builder.Node
	printer     *progress.Printer

	mu     sync.Mutex
	issues map[string][]client.VertexWarning
}

func NewLinter(printer *progress.Printer, failureMode LintFailure, clients []*client.Client, nodes []builder.Node) *Linter {
	return &Linter{
		FailureMode: failureMode,
		Clients:     clients,
		BuildxNodes: nodes,
		printer:     printer,
		issues:      make(map[string][]client.VertexWarning),
	}
}

func (l *Linter) Handle(ctx context.Context, target string, driverIndex int, dockerfile *build.DockerfileInputs, p progress.Writer) error {
	debuglog.Log("Lint Handle() called")
	defer debuglog.Log("Lint Handle() done")

	if l.FailureMode == LintSkip {
		return nil
	}

	// If there is an error parsing the Dockerfile, we'll return it in failure mode;
	// otherwise, we'll print it as an error message.
	if dockerfile.Err != nil && l.FailureMode != LintNone {
		return dockerfile.Err
	}

	if len(dockerfile.Content) == 0 {
		return nil
	}

	// This prevents more than one platform architecture from running linting.
	{
		l.mu.Lock()
		if _, ok := l.issues[target]; ok {
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
	}

	if driverIndex > len(l.Clients) {
		return nil
	}
	if l.Clients[driverIndex] == nil {
		return nil
	}
	if len(l.BuildxNodes[driverIndex].Platforms) == 0 {
		return nil
	}

	lintName := "[lint]"
	if target != defaultTargetName {
		lintName = fmt.Sprintf("[%s lint]", target)
	}
	dgst := digest.Canonical.FromString(identity.NewID())
	tm := time.Now()
	progresshelper.WriteLint(l.printer, client.Vertex{Digest: dgst, Name: lintName, Started: &tm}, nil, nil)

	output, err := RunHadolint(ctx, l.Clients[driverIndex], l.BuildxNodes[driverIndex].Platforms[0], dockerfile)
	if err != nil {
		if l.FailureMode != LintNone {
			return err
		}
	}
	lints := UnmarshalHadolints(&output)

	output, err = RunSemgrep(ctx, l.Clients[driverIndex], l.BuildxNodes[driverIndex].Platforms[0], dockerfile)
	if err != nil {
		if l.FailureMode != LintNone {
			return err
		}
	}
	semgrepLints := UnmarshalSemgreps(&output)
	for _, semgrepLint := range semgrepLints {
		duplicate := false
		for i, hadoLint := range lints {
			if semgrepLint.Line == hadoLint.Line && semgrepLint.SourceRuleURL == hadoLint.SourceRuleURL {
				// Prefer the semgrep message.  It has a lot of great information
				lints[i] = semgrepLint
				duplicate = true
				break
			}
		}

		if !duplicate {
			lints = append(lints, semgrepLint)
		}
	}

	var (
		exceedsFailureSeverity bool
		doneTm                 time.Time              = time.Now() // All lints are "done" at the same time.
		statuses               []*client.VertexStatus              // Prints during the buildkit progress in the context of [lint].
		logs                   []*client.VertexLog                 // Prints when there are `RunLint` errors.
		warnings               []client.VertexWarning              // Prints after the buildkit progress has finished before exit.
	)

	for _, lint := range lints {
		// We are using the iota for both the LintLevel and the LintFailureMode by
		// assuming they are the same numbers for both.
		if int(lint.LintLevel) <= int(l.FailureMode) {
			exceedsFailureSeverity = true
		}

		status := &client.VertexStatus{
			Vertex:    dgst,
			ID:        fmt.Sprintf("%s %s:%d %s: %s", strings.ToUpper(lint.Level), lint.File, lint.Line, lint.Code, lint.Message),
			Timestamp: tm,
			Started:   &tm,
			Completed: &doneTm,
		}
		statuses = append(statuses, status)

		warning := client.VertexWarning{
			Vertex: dgst,
			Level:  int(lint.LintLevel),
			Short:  []byte(lint.Message),
			SourceInfo: &pb.SourceInfo{
				Filename: lint.File,
				Data:     dockerfile.Content,
			},
			Range: []*pb.Range{
				{
					Start: pb.Position{
						Line:      int32(lint.Line),
						Character: int32(lint.Column),
					},
				},
			},
			URL: lint.URL,
		}
		warnings = append(warnings, warning)
	}

	lintResults := client.Vertex{
		Digest:    dgst,
		Name:      lintName,
		Started:   &tm,
		Completed: &doneTm,
	}

	// Report the error from the `RunLint` function up a ways.
	if err != nil {
		lintResults.Error = err.Error()
		log := &client.VertexLog{
			Vertex:    dgst,
			Stream:    1,
			Data:      []byte(err.Error()),
			Timestamp: tm,
		}

		logs = append(logs, log)
	}

	// If we were unable to read the dockerfile at all we'll report it here.
	// Again, this error would come from a ways up this function.
	if dockerfile.Err != nil {
		lintResults.Error = dockerfile.Err.Error()
		log := &client.VertexLog{
			Vertex:    dgst,
			Stream:    1,
			Data:      []byte(dockerfile.Err.Error()),
			Timestamp: tm,
		}

		logs = append(logs, log)
	}

	var lintErr error

	// Collect all failing lint issues to be sent to the API.
	if len(warnings) > 0 && l.FailureMode != LintNone && exceedsFailureSeverity {
		var lintIssues []string

		// This error is not a lint but an error from `RunLint`.
		if lintResults.Error != "" {
			lintIssues = append(lintIssues, lintResults.Error)
		}

		// The status messages have a nicely formatted lint issue string.
		for _, status := range statuses {
			lintIssues = append(lintIssues, status.ID)
		}

		// We join all issues together with a newline so we don't need to update the API honestly.
		// It'll be up to the consumers of the error to parse it.
		lintResults.Error = strings.Join(lintIssues, "\n")
		lintErr = LintFailed
	}

	progresshelper.WriteLint(l.printer, lintResults, statuses, logs)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.issues == nil {
		l.issues = make(map[string][]client.VertexWarning)
	}
	l.issues[target] = warnings

	return lintErr
}

func RunImage(ctx context.Context, imageName string, args []string, c *client.Client, platform ocispecs.Platform, dockerfile *build.DockerfileInputs) (CaptureOutput, error) {
	output := CaptureOutput{}
	_, err := c.Build(ctx, client.SolveOpt{}, "buildx", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		image := llb.Image(imageName).
			Platform(platform).
			File(
				llb.Mkfile(dockerfile.Filename, 0664, dockerfile.Content),
				llb.WithCustomName("[internal] lint"),
			)

		def, err := image.Marshal(ctx, llb.Platform(platform))
		if err != nil {
			return nil, err
		}
		imgRef, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		containerCtx, containerCancel := context.WithCancel(ctx)
		defer containerCancel()
		bkContainer, err := c.NewContainer(containerCtx, gateway.NewContainerRequest{
			Mounts: []gateway.Mount{
				{
					Dest:      "/",
					MountType: pb.MountType_BIND,
					Ref:       imgRef.Ref,
				},
			},
			Platform: &pb.Platform{Architecture: platform.Architecture, OS: platform.OS},
		})
		if err != nil {
			return nil, err
		}

		proc, err := bkContainer.Start(ctx, gateway.StartRequest{
			Args:   args,
			Stdout: &output,
		})
		if err != nil {
			_ = bkContainer.Release(ctx)
			return nil, err
		}
		_ = proc.Wait()

		output.Err = bkContainer.Release(ctx)

		return nil, nil
	}, nil)
	return output, err
}

func RunHadolint(ctx context.Context, client *client.Client, platform ocispecs.Platform, dockerfile *build.DockerfileInputs) (CaptureOutput, error) {
	args := []string{"/bin/hadolint", dockerfile.Filename, "-f", "json"}
	return RunImage(ctx, Hadolint, args, client, platform, dockerfile)
}

func RunSemgrep(ctx context.Context, client *client.Client, platform ocispecs.Platform, dockerfile *build.DockerfileInputs) (CaptureOutput, error) {
	args := []string{"/usr/local/bin/semgrep", "scan", "--config=p/dockerfile", "--json", "--quiet", "--disable-version-check", dockerfile.Filename}
	return RunImage(ctx, Semgrep, args, client, platform, dockerfile)
}

// CaptureOutput is a io.WriteCloser that captures the output of a container.
// The buildkit container start expects that stdout will be written to an io.WriteCloser.
// CaptureOutput collects each newline delimited message and decodes JSON encoded line.
type CaptureOutput struct {
	Messages []string
	Err      error
}

func (o *CaptureOutput) Write(p []byte) (n int, err error) {
	msg := string(p)
	msgs := strings.Split(msg, "\n")
	for i := range msgs {
		if msgs[i] == "" {
			continue
		}
		o.Messages = append(o.Messages, msgs[i])
	}

	return len(p), nil
}

func (o *CaptureOutput) Close() error {
	return nil
}

func UnmarshalHadolints(output *CaptureOutput) []Lint {
	var allLints []Lint
	for _, msg := range output.Messages {
		var lints []Lint
		if err := json.Unmarshal([]byte(msg), &lints); err != nil {
			continue
		}

		for i := range lints {
			lints[i].LintLevel = NewLintLevel(lints[i].Level)
			lints[i].URL = fmt.Sprintf("https://github.com/hadolint/hadolint/wiki/%s", lints[i].Code)
			// SourceRuleURL is used to deduplicate hadolint and semgrep lint issues.
			lints[i].SourceRuleURL = lints[i].URL
		}

		allLints = append(allLints, lints...)
	}
	return allLints
}

type Lint struct {
	Code string `json:"code"`
	// URL is the URL to the lint documentation.
	// It is constructed from other data in the Lint such as `Code`.
	URL string `json:"-"`
	// SourceRuleURL is used to deduplicate hadolint and semgrep issues as the
	// semgrep SourceRuleURL is the same as the hadolint URL field.
	SourceRuleURL string `json:"-"`

	Column int    `json:"column"`
	File   string `json:"file"`
	Level  string `json:"level"`
	// LintLevel is the parsed version of Level after JSON deserialization.
	LintLevel LintLevel `json:"-"`
	Line      int       `json:"line"`
	Message   string    `json:"message"`
}

type LintLevel int

const (
	LintLevelError LintLevel = iota
	LintLevelWarn
	LintLevelInfo
	LintLevelUnknown
)

func NewLintLevel(level string) LintLevel {
	switch strings.ToUpper(level) {
	case "ERROR":
		return LintLevelError
	case "WARNING":
		return LintLevelWarn
	case "INFO":
		return LintLevelInfo
	case "STYLE":
		return LintLevelInfo
	default:
		return LintLevelUnknown
	}
}

func (l LintLevel) String() string {
	switch l {
	case LintLevelError:
		return "ERROR"
	case LintLevelWarn:
		return "WARN"
	case LintLevelInfo:
		return "INFO"
	default:
		return ""
	}
}

func (l LintLevel) Color() aec.ANSI {
	switch l {
	case LintLevelError:
		return aec.RedF
	case LintLevelWarn:
		return aec.YellowF
	case LintLevelInfo:
		return aec.BlueF
	default:
		return aec.DefaultF
	}
}

func UnmarshalSemgreps(output *CaptureOutput) []Lint {
	var lints []Lint
	for _, msg := range output.Messages {
		var results SemgrepOutput
		if err := json.Unmarshal([]byte(msg), &results); err != nil {
			continue
		}

		for _, result := range results.Results {
			lint := Lint{
				Code:          result.Extra.Metadata.SemgrepDev.Rule.RuleID,
				URL:           result.Extra.Metadata.Source,
				SourceRuleURL: result.Extra.Metadata.SourceRuleURL,
				Column:        result.Start.Col,
				File:          result.Path,
				Level:         result.Extra.Severity,
				Line:          result.Start.Line,
				Message:       result.Extra.Message,
			}
			lints = append(lints, lint)
		}
	}

	for i := range lints {
		lints[i].LintLevel = NewLintLevel(lints[i].Level)
	}

	return lints
}

type SemgrepOutput struct {
	Results []Results `json:"results"`
}

type Position struct {
	Col    int `json:"col"`
	Line   int `json:"line"`
	Offset int `json:"offset"`
}

type Rule struct {
	RuleID string `json:"rule_id"`
}

type SemgrepDev struct {
	Rule Rule `json:"rule"`
}

type Metadata struct {
	Source        string     `json:"source"`
	SourceRuleURL string     `json:"source-rule-url"`
	SemgrepDev    SemgrepDev `json:"semgrep.dev"`
}
type Extra struct {
	Lines    string   `json:"lines"`
	Message  string   `json:"message"`
	Metadata Metadata `json:"metadata"`
	Severity string   `json:"severity"`
}

type Results struct {
	Start Position `json:"start"`
	Extra Extra    `json:"extra"`
	Path  string   `json:"path"`
}

func (l *Linter) Print(w io.Writer, mode string) {
	// Copied from printWarnings with a few modifications for errors.
	if l.FailureMode == LintSkip {
		return
	}

	if mode == progress.PrinterModeQuiet {
		return
	}

	numIssues := 0
	for _, targetIssues := range l.issues {
		numIssues += len(targetIssues)
	}
	if numIssues == 0 {
		return
	}

	fmt.Fprintf(w, "\n ")
	summary := "1 linter issue found"
	if numIssues > 1 {
		summary = fmt.Sprintf("%d linter issues found", numIssues)
	}

	if mode != progress.PrinterModePlain {
		summary = l.FailureMode.Color().Apply(summary)
	}
	fmt.Fprintf(w, "%s:\n", summary)

	for target, issues := range l.issues {
		if target == defaultTargetName {
			target = ""
		} else {
			target = fmt.Sprintf("[%s] ", target)
		}

		for _, issue := range issues {
			lintLevel := LintLevel(issue.Level)
			level := lintLevel.String()
			if mode != progress.PrinterModePlain {
				level = lintLevel.Color().Apply(level)
			}

			fmt.Fprintf(w, "%s %s%s:%d %s\n", level, target, issue.SourceInfo.Filename, issue.Range[0].Start.Line, issue.Short)

			for _, d := range issue.Detail {
				fmt.Fprintf(w, "%s\n", d)
			}

			PrintURLLink(w, "  More info", issue.URL, mode)

			if issue.SourceInfo != nil && issue.Range != nil {
				PrintFileContext(w, &issue, lintLevel, mode)
			}
			fmt.Fprintf(w, "\n")

		}
	}
}

func PrintFileContext(w io.Writer, issue *client.VertexWarning, lintColor LintLevel, progressMode string) {
	si := issue.SourceInfo
	if si == nil {
		return
	}
	lines := strings.Split(string(si.Data), "\n")

	start, end, ok := getStartEndLine(issue.Range)
	if !ok {
		return
	}
	if start > len(lines) || start < 1 {
		return
	}
	if end > len(lines) {
		end = len(lines)
	}

	pad := 2
	if end == start {
		pad = 4
	}

	var p int
	for {
		if p >= pad {
			break
		}
		if start > 1 {
			start--
			p++
		}
		if end != len(lines) {
			end++
			p++
		}
		p++
	}

	fmt.Fprint(w, "\n  --------------------\n")
	for i := start; i <= end; i++ {
		pfx := "   "
		if containsLine(issue.Range, i) {
			pfx = ">>>"
			if progressMode != progress.PrinterModePlain {
				pfx = lintColor.Color().Apply(pfx)
			}
		}
		fmt.Fprintf(w, "   %3d | %s %s\n", i, pfx, lines[i-1])
	}
	fmt.Fprintf(w, "  --------------------\n")
}

func containsLine(rr []*pb.Range, l int) bool {
	for _, r := range rr {
		e := r.End.Line
		if e < r.Start.Line {
			e = r.Start.Line
		}
		if r.Start.Line <= int32(l) && e >= int32(l) {
			return true
		}
	}
	return false
}

func getStartEndLine(rr []*pb.Range) (start int, end int, ok bool) {
	first := true
	for _, r := range rr {
		e := r.End.Line
		if e < r.Start.Line {
			e = r.Start.Line
		}
		if first || int(r.Start.Line) < start {
			start = int(r.Start.Line)
		}
		if int(e) > end {
			end = int(e)
		}
		first = false
	}
	return start, end, !first
}
