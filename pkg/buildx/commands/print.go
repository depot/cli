package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"

	"github.com/depot/cli/pkg/compose"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/mgutz/ansi"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/lint"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	solverpb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/savioxavier/termlink"
	"google.golang.org/protobuf/proto"
)

func BakePrint(dockerCli command.Cli, targets []string, in BakeOptions) error {
	if len(targets) == 0 {
		targets = []string{"default"}
	}

	files, err := bake.ReadLocalFiles(in.files, os.Stdin, nil)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("couldn't find a bake definition")
	}

	composeTargets, err := compose.Targets(files)
	if err != nil {
		return err
	}
	defaults := bakeDefaults("cwd://")
	tgts, grps, projects, err := readProjectTargets(context.Background(), files, composeTargets, targets, overrides(in), defaults)
	if err != nil {
		return err
	}
	if err := readTargetDescriptions(context.Background(), files, composeTargets, tgts, targets, overrides(in), defaults); err != nil {
		return err
	}
	setSourceDateEpoch(tgts)

	printedTargets := make(map[string]json.RawMessage, len(tgts))
	for name, target := range tgts {
		dt, err := json.Marshal(target)
		if err != nil {
			return err
		}
		if projectID, ok := projects[name]; ok {
			dt, err = appendJSONField(dt, projectIDAttribute, projectID)
			if err != nil {
				return err
			}
		}
		printedTargets[name] = dt
	}

	dt, err := json.MarshalIndent(struct {
		Group  map[string]*bake.Group     `json:"group,omitempty"`
		Target map[string]json.RawMessage `json:"target"`
	}{grps, printedTargets}, "", "  ")
	if err != nil {
		return err
	}

	fmt.Fprintln(dockerCli.Out(), string(dt))
	return nil
}

// appendJSONField adds a field after the last field of a JSON object.
func appendJSONField(object []byte, key, value string) ([]byte, error) {
	field, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		return nil, err
	}
	object = bytes.TrimSuffix(bytes.TrimSpace(object), []byte("}"))
	field = bytes.TrimPrefix(field, []byte("{"))
	if len(bytes.TrimSpace(object)) > 1 {
		object = append(object, ',')
	}
	return append(object, field...), nil
}

// printCallResults prints the result of each target that has a call
// request. With names, each result follows the name of its target, as in
// buildx bake. It returns an error with the status code that a request
// reports.
func printCallResults(w io.Writer, opts map[string]build.Options, responses map[string]*client.SolveResponse, withNames bool) error {
	exitCode := 0
	printed := false
	for _, name := range slices.Sorted(maps.Keys(opts)) {
		opt := opts[name]
		if opt.CallFunc == nil {
			continue
		}
		var res map[string]string
		if r, ok := responses[name]; ok && r != nil {
			res = r.ExporterResponse
		}
		if withNames {
			if printed {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s\n\n", name)
		}
		printed = true
		code, err := printResult(w, opt.CallFunc, res, &opt.Inputs)
		if err != nil {
			if !withNames {
				return err
			}
			fmt.Fprintf(w, "error: %v\n", err)
			code = 1
		}
		if code != 0 && exitCode == 0 {
			exitCode = code
		}
	}
	if exitCode != 0 {
		return cli.StatusError{StatusCode: exitCode}
	}
	return nil
}

// printResult prints the result of a call request such as outline, targets,
// or check, in the same way as buildx. It returns the status code that the
// request reports.
func printResult(w io.Writer, f *build.CallFunc, res map[string]string, inp *build.Inputs) (int, error) {
	switch f.Name {
	case "outline":
		return 0, printValue(w, outline.PrintOutline, outline.SubrequestsOutlineDefinition.Version, f.Format, res)
	case "targets":
		return 0, printValue(w, targets.PrintTargets, targets.SubrequestsTargetsDefinition.Version, f.Format, res)
	case "subrequests.describe":
		return 0, printValue(w, subrequests.PrintDescribe, subrequests.SubrequestsDescribeDefinition.Version, f.Format, res)
	case "lint":
		lintResults := lint.LintResults{}
		if result, ok := res["result.json"]; ok {
			if err := json.Unmarshal([]byte(result), &lintResults); err != nil {
				return 0, err
			}
		}

		warningCount := len(lintResults.Warnings)
		if f.Format != "json" && warningCount > 0 {
			warningCountMsg := "1 warning has been found!"
			if warningCount > 1 {
				warningCountMsg = fmt.Sprintf("%d warnings have been found!", warningCount)
			}
			fmt.Fprintf(w, "Check complete, %s\n", warningCountMsg)
		}
		sourceInfoMap := func(sourceInfo *solverpb.SourceInfo) *solverpb.SourceInfo {
			if sourceInfo == nil || inp == nil || inp.DockerfileMappingSrc == "" {
				return sourceInfo
			}
			newSourceInfo := proto.Clone(sourceInfo).(*solverpb.SourceInfo)
			newSourceInfo.Filename = inp.DockerfileMappingSrc
			return newSourceInfo
		}
		printLintWarnings := func(_ []byte, w io.Writer) error {
			return lintResults.PrintTo(w, sourceInfoMap)
		}
		if err := printValue(w, printLintWarnings, lint.SubrequestLintDefinition.Version, f.Format, res); err != nil {
			return 0, err
		}

		if lintResults.Error != nil {
			if f.Format != "json" && warningCount > 0 {
				fmt.Fprintln(w)
			}
			lintBuf := bytes.NewBuffer(nil)
			lintResults.PrintErrorTo(lintBuf, sourceInfoMap)
			return 0, errors.New(lintBuf.String())
		} else if warningCount == 0 && f.Format != "json" {
			fmt.Fprintln(w, "Check complete, no warnings found.")
		}
	default:
		if dt, ok := res["result.json"]; ok && f.Format == "json" {
			fmt.Fprintln(w, dt)
		} else if dt, ok := res["result.txt"]; ok {
			fmt.Fprint(w, dt)
		} else {
			fmt.Fprintf(w, "%s %+v\n", f.Name, res)
		}
	}
	if v, ok := res["result.statuscode"]; !f.IgnoreStatus && ok {
		if n, err := strconv.Atoi(v); err == nil && n != 0 {
			return n, nil
		}
	}
	return 0, nil
}

type printFunc func([]byte, io.Writer) error

func printValue(w io.Writer, printer printFunc, version string, format string, res map[string]string) error {
	if format == "json" {
		fmt.Fprintln(w, res["result.json"])
		return nil
	}

	if res["version"] != "" && versions.LessThan(version, res["version"]) && res["result.txt"] != "" {
		// structure is too new and we don't know how to print it
		fmt.Fprint(w, res["result.txt"])
		return nil
	}
	return printer([]byte(res["result.json"]), w)
}

func PrintBuildURL(buildURL, progress string) {
	if os.Getenv("DEPOT_NO_SUMMARY_LINK") != "" || os.Getenv("DEPOT_IN_AUTOMATION") != "" {
		return
	}
	PrintURLLink(os.Stderr, "\nBuild Summary", buildURL, progress)
}

// PrintURLLink will print a link that is clickable in supported terminals.
func PrintURLLink(w io.Writer, title, url, progress string) {
	if url != "" {
		if progress == string(progressui.PlainMode) {
			fmt.Fprintf(w, "%s: %s\n", title, url)
		} else {
			title := ansi.Color(title, "cyan+b")
			if termlink.SupportsHyperlinks() {
				url = termlink.Link(url, url)
			} else {
				url = ansi.Color(url, "default+u")
			}
			fmt.Fprintf(w, "%s: %s\n", title, url)
		}
	}
}
