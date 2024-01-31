// Source: https://github.com/docker/buildx/blob/v0.10/commands/print.go

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/containerd/containerd/platforms"
	"github.com/depot/cli/pkg/buildx/bake"
	"github.com/docker/buildx/build"
	buildxprogress "github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/docker/docker/api/types/versions"
	"github.com/mgutz/ansi"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/savioxavier/termlink"
)

func BakePrint(dockerCli command.Cli, targets []string, in BakeOptions) (err error) {
	if len(targets) == 0 {
		targets = []string{"default"}
	}

	files, err := bake.ReadLocalFiles(in.files)
	if err != nil {
		return err
	}

	overrides := overrides(in)
	defaults := map[string]string{
		"BAKE_CMD_CONTEXT":    "cwd://",
		"BAKE_LOCAL_PLATFORM": platforms.DefaultString(),
	}
	tgts, grps, err := bake.ReadTargets(context.Background(), files, targets, overrides, defaults)
	if err != nil {
		return err
	}

	dt, err := json.MarshalIndent(BakePrintOutput{grps, tgts}, "", "  ")
	if err != nil {
		return err
	}

	fmt.Fprintln(dockerCli.Out(), string(dt))
	return nil
}

type BakePrintOutput struct {
	Group  map[string]*bake.Group  `json:"group,omitempty"`
	Target map[string]*bake.Target `json:"target"`
}

func printResult(f *build.PrintFunc, res map[string]string) error {
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
			log.Printf("%s %+v", f, res)
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
	if os.Getenv("DEPOT_NO_SUMMARY_LINK") != "" {
		return
	}
	PrintURLLink(os.Stderr, "\nBuild Summary", buildURL, progress)
}

// PrintURLLink will print a link that is clickable in supported terminals.
func PrintURLLink(w io.Writer, title, url, progress string) {
	if url != "" {
		if progress == buildxprogress.PrinterModePlain {
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
