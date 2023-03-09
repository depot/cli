package commands

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	cliv1connect "github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ progress.Writer = (*Progress)(nil)

type Progress struct {
	buildID string
	token   string

	client   cliv1connect.BuildServiceClient
	vertices chan []*client.Vertex

	p *progress.Printer
}

func NewProgress(ctx context.Context, buildID, token, progressMode string) (*Progress, error) {
	// Buffer up to 1024 vertex slices before blocking the build.
	const channelBufferSize = 1024
	p, err := progress.NewPrinter(ctx, os.Stderr, os.Stderr, progressMode)
	if err != nil {
		return nil, err
	}

	return &Progress{
		buildID:  buildID,
		token:    token,
		client:   depotapi.NewBuildClient(),
		vertices: make(chan []*client.Vertex, channelBufferSize),
		p:        p,
	}, nil
}

func (p *Progress) Write(s *client.SolveStatus) {
	select {
	case p.vertices <- s.Vertexes:
	default:
		// if channel is full skip recording vertex time to prevent blocking the build.
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
	// Buffer 1 second before sending build timings to the server
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
				if _, ok := uniqueVertices[v.Digest]; ok {
					continue
				}

				uniqueVertices[v.Digest] = struct{}{}
				step := NewStep(v)
				steps = append(steps, &step)
			}
		case <-ticker.C:
			Analyze(steps)
			// Requires a new context because the previous one may be canceled while we are
			// sending the build timings.  At most one will wait 5 seconds.
			ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.ReportBuildSteps(ctx2, steps)
			ticker.Reset(bufferTimeout)
			cancel()
		case <-ctx.Done():
			// Send all remaining build timings before exiting.
			for {
				select {
				case vs := <-p.vertices:
					for _, v := range vs {
						if v == nil || v.Started == nil || v.Completed == nil {
							continue
						}

						if _, ok := uniqueVertices[v.Digest]; ok {
							continue
						}

						uniqueVertices[v.Digest] = struct{}{}
						step := NewStep(v)
						steps = append(steps, &step)
					}
				default:
					Analyze(steps)
					// Requires a new context because the previous one was canceled.
					ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					p.ReportBuildSteps(ctx2, steps)
					cancel()

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

	req := NewTimingRequest(p.buildID, steps)
	if req == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.client.ReportTimings(ctx, depotapi.WithAuthentication(connect.NewRequest(req), p.token))

	if err != nil {
		// No need to log errors to the user as it is fine if we miss some build timings.
		return
	} else {
		// Mark the steps as reported if the request was successful.
		for i := range steps {
			steps[i].Reported = true
		}
	}
}

// Analyze computes the input duration for each step.
func Analyze(steps []*Step) {
	// Used to lookup the index of a step's input digests.
	digestIdx := make(map[digest.Digest]int, len(steps))
	for i := range steps {
		digestIdx[steps[i].Digest] = i
		digestIdx[steps[i].StableDigest] = i
	}

	stableDigests := make(map[digest.Digest]digest.Digest, len(steps))
	for i := range steps {
		// Filters out "random" digests that are not stable.
		// An example of this is:
		// { "Name": "[internal] load build context", "StableDigest": "random:8831e066dc1584a0ff85128626b574bcb4bf68e46ab71957522169d84586768d" }

		if !strings.HasPrefix(steps[i].StableDigest.String(), "random:") {
			stableDigests[steps[i].Digest] = steps[i].StableDigest
		}
	}

	// Discover all stable input digests for each step.
	for i := range steps {
		// Already analyzed (minor optimization).
		if len(steps[i].StableInputDigests) > 0 {
			continue
		}

		for _, inputDigest := range steps[i].InputDigests {
			if stableDigest, ok := stableDigests[inputDigest]; ok {
				steps[i].StableInputDigests = append(steps[i].StableInputDigests, stableDigest)
			}
		}
	}

	// Discover all stable ancestor digests for each step.
	for i := range steps {
		// Already analyzed (minor optimization).
		if len(steps[i].AncestorDigests) > 0 {
			continue
		}

		// Using the StableInputDigests filters out any vertex without a stable digest.
		ancestorDigests := make(map[digest.Digest]struct{}, len(steps[i].InputDigests))

		stack := make([]digest.Digest, len(steps[i].InputDigests))
		copy(stack, steps[i].InputDigests)

		var stepDigest digest.Digest
		// Depth first traversal reading from leaves to first cached vertex or to root.
		for {
			if len(stack) == 0 {
				break
			}

			for j := range stack {
				ancestorDigests[stack[j]] = struct{}{}
			}

			stepDigest, stack = stack[len(stack)-1], stack[:len(stack)-1]
			idx, ok := digestIdx[stepDigest]
			if !ok {
				// Missing vertex; not sure this happens.
				continue
			}

			step := steps[idx]
			stack = append(stack, step.InputDigests...)
		}

		for ancestor := range ancestorDigests {
			if stableAncestor, ok := stableDigests[ancestor]; ok {
				steps[i].AncestorDigests = append(steps[i].AncestorDigests, stableAncestor)
			}
		}

		// Sort the ancestor digests to ensure that the order is consistent.
		// Order is the same as the order of the input digests.
		// Effectively this is a topological sort.
		sort.Slice(steps[i].AncestorDigests, func(j, k int) bool {
			return digestIdx[steps[i].AncestorDigests[j]] <
				digestIdx[steps[i].AncestorDigests[k]]
		})
	}
}

func NewTimingRequest(buildID string, steps []*Step) *cliv1.ReportTimingsRequest {
	buildSteps := make([]*cliv1.BuildStep, 0, len(steps))
	for _, step := range steps {
		// Skip steps that have already been reported.
		if step.Reported {
			continue
		}

		buildStep := &cliv1.BuildStep{
			StartTime:  timestamppb.New(step.StartTime),
			DurationMs: int32(step.Duration.Milliseconds()),
			Name:       step.Name,
			Cached:     step.Cached,
		}

		if step.Error != "" {
			buildStep.Error = &step.Error
		}

		stableDigest := step.StableDigest.String()
		// Do not report "random" digests such as local build context.
		if !strings.HasPrefix(stableDigest, "random:") && stableDigest != "" {
			buildStep.StableDigest = &stableDigest
		}

		for _, stableInputDigest := range step.StableInputDigests {
			buildStep.InputDigests = append(buildStep.InputDigests, stableInputDigest.String())
		}

		for _, ancestor := range step.AncestorDigests {
			buildStep.AncestorDigests = append(buildStep.AncestorDigests, ancestor.String())
		}

		buildSteps = append(buildSteps, buildStep)
	}

	if len(buildSteps) == 0 {
		return nil
	}

	req := &cliv1.ReportTimingsRequest{
		BuildId:    buildID,
		BuildSteps: buildSteps,
	}

	return req
}

// Step is one of those internal data structures that translates domains from
// buildkitd to CLI and from CLI to the API server.
type Step struct {
	Name string

	Digest       digest.Digest // Buildkit digest is hashed with random inputs to create random input digests.
	StableDigest digest.Digest // Stable digest is the same for the same inputs. This is a depot extension.

	StartTime time.Time
	Duration  time.Duration

	Cached bool
	Error  string

	InputDigests       []digest.Digest // Buildkit input digests are hashed with random inputs to create random input digests.
	StableInputDigests []digest.Digest // Stable input digests are the same for the same inputs.
	AncestorDigests    []digest.Digest // Ancestor digests are the input digests of all previous steps.

	Reported bool
}

// Assumes that Completed and Started are not nil.
func NewStep(v *client.Vertex) Step {
	step := Step{
		Name:         v.Name,
		Digest:       v.Digest,
		StartTime:    *v.Started,
		Duration:     v.Completed.Sub(*v.Started),
		Cached:       v.Cached,
		StableDigest: v.StableDigest,
		Error:        v.Error,
		InputDigests: v.Inputs,
	}

	return step
}

// Instruction is the parsed instruction from a build step.
type Instruction struct {
	Platform string
	Stage    string
	Step     int
	Total    int
}
