package commands

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ progress.Writer = (*Progress)(nil)

type Progress struct {
	buildID string
	token   string

	client   cliv1beta1connect.BuildServiceClient
	vertices chan []*client.Vertex

	p *progress.Printer
}

func NewProgress(ctx context.Context, c cliv1beta1connect.BuildServiceClient, buildID, token, progressMode string) (*Progress, error) {
	// Buffer up to 1024 vertex slices before blocking the build.
	const channelBufferSize = 1024
	p, err := progress.NewPrinter(ctx, os.Stderr, os.Stderr, progressMode)
	if err != nil {
		return nil, err
	}

	return &Progress{
		buildID:  buildID,
		token:    token,
		client:   c,
		vertices: make(chan []*client.Vertex, channelBufferSize),
		p:        p,
	}, nil
}

func (p *Progress) Write(s *client.SolveStatus) {
	select {
	case p.vertices <- s.Vertexes:
	default:
		// if channel is full skip recording vertex time to prevent blocking the build.
		log.Printf("Skip recording of build timing")
	}

	p.p.Write(s)
}

func (p *Progress) ValidateLogSource(digest digest.Digest, v interface{}) bool {
	return p.p.ValidateLogSource(digest, v)
}

func (p *Progress) ClearLogSource(v interface{}) {
	p.p.ClearLogSource(v)
}

func (p *Progress) Wait() error {
	return p.p.Wait()
}

func (p *Progress) Warnings() []client.VertexWarning {
	return p.p.Warnings()
}

// Run should be started in a go routine to send build timings to the server on a timer.
//
// Cancel the context to stop the go routine.
func (p *Progress) Run(ctx context.Context) {
	// Buffer 5 seconds before sending build timings to the server
	const (
		bufferTimeout = time.Second
	)

	ticker := time.NewTicker(bufferTimeout)
	defer ticker.Stop()

	// The same vertex can be reported multiple times.  The data appears
	// the same except for the duration.  It's not clear to me if we should
	// merge the durations or just use the first one.
	uniqueVertices := map[digest.Digest]struct{}{}
	steps := []*Step{}

	for {
		select {
		case vs := <-p.vertices:
			for _, v := range vs {
				// Only record vertices that have completed.
				if v == nil || v.Started == nil || v.Completed == nil {
					continue
				}

				// skip if recorded already.
				if _, ok := uniqueVertices[v.StableDigest]; ok {
					continue
				}

				uniqueVertices[v.StableDigest] = struct{}{}
				step := NewStep(v)
				steps = append(steps, &step)
			}

			p.Analyze(steps)
		case <-ticker.C:
			p.ReportBuildSteps(ctx, steps)
			ticker.Reset(bufferTimeout)
		case <-ctx.Done():
			// Send all remaining build timings before exiting.
			for {
				select {
				case vs := <-p.vertices:
					for _, v := range vs {
						if v == nil || v.Started == nil || v.Completed == nil {
							continue
						}

						if _, ok := uniqueVertices[v.StableDigest]; ok {
							continue
						}

						uniqueVertices[v.StableDigest] = struct{}{}
						step := NewStep(v)
						steps = append(steps, &step)
					}

					p.Analyze(steps)
				default:
					p.ReportBuildSteps(ctx, steps)
					return
				}
			}
		}
	}
}

func (p *Progress) ReportBuildSteps(ctx context.Context, steps []*Step) {
	if len(steps) == 0 {
		return
	}

	buildSteps := make([]*cliv1beta1.BuildStep, 0, len(steps))
	for _, step := range steps {
		if step.Reported {
			continue
		}

		buildStep := &cliv1beta1.BuildStep{
			Name:          step.Name,
			StableDigest:  step.StableDigest.String(),
			StartTime:     timestamppb.New(step.StartTime),
			Duration:      step.Duration.Microseconds(),
			InputDuration: step.InputDuration.Microseconds(),
			Cached:        step.Cached,
			Error:         &step.Error,
		}

		if step.Command != nil {
			buildStep.Command = &cliv1beta1.Command{
				Platform:   &step.Command.Platform,
				Stage:      &step.Command.Stage,
				Step:       int64(step.Command.Step),
				TotalSteps: int64(step.Command.Total),
			}
		}

		buildSteps = append(buildSteps, buildStep)
	}

	if len(buildSteps) == 0 {
		return
	}

	req := &cliv1beta1.ReportTimingsRequest{
		BuildId:    p.buildID,
		BuildSteps: buildSteps,
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.client.ReportTimings(ctx, depotapi.WithAuthentication(connect.NewRequest(req), p.token))

	if err != nil {
		log.Printf("Failed to report build timings: %v", err)
	} else {
		// Mark the steps as reported if the request was successful.
		for i := range steps {
			steps[i].Reported = true
		}
	}
}

// Analyze computes the input duration for each step.
func (p *Progress) Analyze(steps []*Step) {
	// Used to lookup the index of a step's input digests.
	digestIdx := make(map[digest.Digest]int, len(steps))
	for i := range steps {
		digestIdx[steps[i].StableDigest] = i
	}

	for i := range steps {
		if steps[i].Cached {
			// This is the time to load only the last cached layer of the build.
			// Buildkit reports previous cached layers as zero duration.
			steps[i].InputDuration = steps[i].Duration
			continue
		}

		stack := make([]digest.Digest, len(steps[i].InputDigests))
		copy(stack, steps[i].InputDigests)

		totalDuration := steps[i].Duration
		var stepDigest digest.Digest

		// Depth first traversal reading from leaves to first cached vertex or to root.
		for {
			if len(stack) == 0 {
				break
			}

			stepDigest, stack = stack[len(stack)-1], stack[:len(stack)-1]
			idx, ok := digestIdx[stepDigest]
			if !ok {
				// Missing vertex; not sure this happens.
				continue
			}

			step := steps[idx]

			if step.Cached {
				continue
			}

			if step.InputDuration != 0 {
				// Already visited this node and its input vertices (minor optimization).
				totalDuration += step.InputDuration
			} else {
				totalDuration += step.Duration

				// Reallocate if stack size is too small.
				if cap(stack)-len(stack) < len(step.InputDigests) {
					stack = append(make([]digest.Digest, 0, len(stack)+len(step.InputDigests)), stack...)
				}
				copy(stack, step.InputDigests)
			}
		}

		steps[i].InputDuration = totalDuration
	}
}

type Step struct {
	Name          string
	StableDigest  digest.Digest
	StartTime     time.Time
	Duration      time.Duration
	InputDigests  []digest.Digest
	InputDuration time.Duration

	Command *Command

	Cached   bool
	Error    string
	Reported bool
}

// Assumes that Completed and Started are not nil.
func NewStep(v *client.Vertex) Step {
	step := Step{
		Name:         v.Name,
		StartTime:    *v.Started,
		Duration:     v.Completed.Sub(*v.Started),
		Cached:       v.Cached,
		StableDigest: v.StableDigest,
		InputDigests: v.Inputs,
		Error:        v.Error,
	}

	cmd, found := ParseCommand(v.Name)
	if found {
		step.Command = &cmd
	}

	return step
}

type Command struct {
	Platform string
	Stage    string
	Step     int
	Total    int
}

func ParseCommand(s string) (cmd Command, found bool) {
	s, found = CutCommand(s)
	if !found {
		return
	}

	cmd, _, found = CutBuildStep(s)
	return
}

func CutCommand(s string) (after string, found bool) {
	return CutPrefix(s, "[")
}

func CutBuildStep(s string) (cmd Command, after string, found bool) {
	before, after, found := strings.Cut(s, "] ")
	if !found {
		return Command{}, s, false
	}

	split := strings.Split(before, " ")
	if len(split) == 0 {
		return Command{}, after, false
	}

	// The last element is the step/total.
	stepTotal := strings.Split(split[len(split)-1], "/")
	if len(stepTotal) != 2 {
		return Command{}, after, false
	}

	cmd.Step, _ = strconv.Atoi(stepTotal[0])
	cmd.Total, _ = strconv.Atoi(stepTotal[1])
	if cmd.Step == 0 || cmd.Total == 0 {
		return Command{}, after, false
	}

	for i := 0; i < len(split)-1; i++ {
		// Protect against steps with more than one space such as  1/10.
		if len(split[i]) == 0 {
			continue
		}

		// Platform is the only element that contains a slash.
		// Some architectures contain multiple slashes such as linux/arm64/v7.
		// Additionally, when a platform is emulated then the platform is
		// contains an "arrow" like this: linux/amd64->ppc64le.
		if strings.Contains(split[i], "/") {
			cmd.Platform = split[i]
		} else {
			cmd.Stage = split[i]
		}
	}

	return
}

func CutPrefix(s, prefix string) (after string, found bool) {
	if !strings.HasPrefix(s, prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
