package pull

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/bufbuild/connect-go"
	"github.com/containerd/console"
	"github.com/depot/cli/pkg/api"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/docker/buildx/util/logutil"
	prog "github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func NewCmdPull(dockerCli command.Cli) *cobra.Command {
	var (
		token     string
		projectID string
		platform  string
		buildID   string
		progress  string
		userTags  []string
	)

	cmd := &cobra.Command{
		Use:   "pull [flags] [buildID]",
		Short: "Pull a project's build from the Depot registry",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				buildID = args[0]
			}
			_, isCI := ci.Provider()
			if progress == prog.PrinterModeAuto && isCI {
				progress = prog.PrinterModePlain
			}

			ctx := cmd.Context()

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if buildID == "" {
				var selectedProject *helpers.SelectedProject
				projectID = helpers.ResolveProjectID(projectID)
				if projectID == "" { // No locally saved depot.json.
					selectedProject, err = helpers.OnboardProject(ctx, token)
					if err != nil {
						return err
					}
				} else {
					selectedProject, err = helpers.ProjectExists(ctx, token, projectID)
					if err != nil {
						return err
					}
				}
				projectID = selectedProject.ID

				client := api.NewBuildClient()

				if !helpers.IsTerminal() {
					depotBuilds, err := helpers.Builds(ctx, token, projectID, client)
					if err != nil {
						return err
					}
					_ = depotBuilds.WriteCSV()
					return errors.New("build ID must be specified")
				}

				buildID, err = helpers.SelectBuildID(ctx, token, projectID, client)
				if err != nil {
					return err
				}

				if buildID == "" {
					return errors.New("build ID must be specified")
				}
			}

			client := depotapi.NewBuildClient()
			req := &cliv1.GetPullInfoRequest{BuildId: buildID}
			res, err := client.GetPullInfo(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return err
			}

			opts := load.PullOptions{
				UserTags:  userTags,
				Quiet:     progress == prog.PrinterModeQuiet,
				KeepImage: true,
			}
			if platform != "" {
				opts.Platform = &platform
			}

			opts.Username = &res.Msg.Username
			opts.Password = &res.Msg.Password

			displayPhrase := fmt.Sprintf("Pulling image %s", res.Msg.Reference)

			printerCtx, cancel := context.WithCancel(ctx)
			printer, err := NewPrinter(printerCtx, displayPhrase, os.Stderr, os.Stderr, progress)
			if err != nil {
				cancel()
				return err
			}

			defer func() {
				cancel()
				_ = printer.Wait()
			}()

			err = load.PullImages(ctx, dockerCli.Client(), res.Msg.Reference, opts, printer)
			if err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")
	cmd.Flags().StringVar(&platform, "platform", "", `Pulls image for specific platform ("linux/amd64", "linux/arm64")`)
	cmd.Flags().StringSliceVarP(&userTags, "tag", "t", nil, "Optional tags to apply to the image")
	cmd.Flags().StringVar(&progress, "progress", "auto", `Set type of progress output ("auto", "plain", "tty", "quiet")`)

	return cmd
}

// Specialized printer as the default buildkit one has a hard-coded display phrase, "Building.""
type Printer struct {
	status       chan *client.SolveStatus
	done         <-chan struct{}
	err          error
	warnings     []client.VertexWarning
	logMu        sync.Mutex
	logSourceMap map[digest.Digest]interface{}
}

func (p *Printer) Wait() error                      { close(p.status); <-p.done; return p.err }
func (p *Printer) Write(s *client.SolveStatus)      { p.status <- s }
func (p *Printer) Warnings() []client.VertexWarning { return p.warnings }

func (p *Printer) ValidateLogSource(dgst digest.Digest, v interface{}) bool {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	src, ok := p.logSourceMap[dgst]
	if ok {
		if src == v {
			return true
		}
	} else {
		p.logSourceMap[dgst] = v
		return true
	}
	return false
}

func (p *Printer) ClearLogSource(v interface{}) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	for d := range p.logSourceMap {
		if p.logSourceMap[d] == v {
			delete(p.logSourceMap, d)
		}
	}
}

func NewPrinter(ctx context.Context, displayPhrase string, w io.Writer, out console.File, mode string) (*Printer, error) {
	statusCh := make(chan *client.SolveStatus)
	doneCh := make(chan struct{})

	pw := &Printer{
		status:       statusCh,
		done:         doneCh,
		logSourceMap: map[digest.Digest]interface{}{},
	}

	if v := os.Getenv("BUILDKIT_PROGRESS"); v != "" && mode == prog.PrinterModeAuto {
		mode = v
	}

	var c console.Console
	switch mode {
	case prog.PrinterModeQuiet:
		w = io.Discard
	case prog.PrinterModeAuto, prog.PrinterModeTty:
		if cons, err := console.ConsoleFromFile(out); err == nil {
			c = cons
		} else {
			if mode == prog.PrinterModeTty {
				return nil, errors.Wrap(err, "failed to get console")
			}
		}
	}

	go func() {
		resumeLogs := logutil.Pause(logrus.StandardLogger())
		// not using shared context to not disrupt display but let is finish reporting errors
		// DEPOT: allowed displayPhrase to be overridden.
		pw.warnings, pw.err = progressui.DisplaySolveStatus(ctx, displayPhrase, c, w, statusCh)
		resumeLogs()
		close(doneCh)
	}()

	return pw, nil
}
