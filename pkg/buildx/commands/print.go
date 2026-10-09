package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/depot/cli/pkg/compose"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/cli/cli/command"
	"github.com/mgutz/ansi"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/savioxavier/termlink"
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

	defaults := bakeDefaults("cwd://")
	tgts, grps, err := bake.ReadTargets(context.Background(), withoutProjectIDs(files), targets, overrides(in), defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return err
	}

	projects, err := readTargetProjects(context.Background(), files, targets, defaults)
	if err != nil {
		return err
	}
	composeTargets, err := compose.Targets(files)
	if err != nil {
		return err
	}
	for name, target := range composeTargets {
		if target.ProjectID != "" {
			projects[name] = target.ProjectID
		}
	}

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

func printResult(f *build.CallFunc, res map[string]string) error {
	switch f.Name {
	case "outline":
		return printValue(outline.PrintOutline, outline.SubrequestsOutlineDefinition.Version, f.Format, res)
	case "targets":
		return printValue(targets.PrintTargets, targets.SubrequestsTargetsDefinition.Version, f.Format, res)
	case "subrequests.describe":
		return printValue(subrequests.PrintDescribe, subrequests.SubrequestsDescribeDefinition.Version, f.Format, res)
	default:
		if dt, ok := res["result.txt"]; ok {
			fmt.Print(dt)
		} else {
			log.Printf("%v %+v", f, res)
		}
	}
	return nil
}

type printFunc func([]byte, io.Writer) error

func printValue(printer printFunc, version string, format string, res map[string]string) error {
	if format == "json" {
		fmt.Fprintln(os.Stdout, res["result.json"])
		return nil
	}

	if res["version"] != "" && versions.LessThan(version, res["version"]) && res["result.txt"] != "" {
		// structure is too new and we don't know how to print it
		fmt.Fprint(os.Stdout, res["result.txt"])
		return nil
	}
	return printer([]byte(res["result.json"]), os.Stdout)
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
