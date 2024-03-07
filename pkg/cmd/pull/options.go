package pull

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/load"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	prog "github.com/docker/buildx/util/progress"
	"golang.org/x/exp/slices"
)

func isSavedBuild(options []*cliv1.BuildOptions) bool {
	for _, opt := range options {
		if opt.Save {
			return true
		}
	}
	return false
}

func isBake(options []*cliv1.BuildOptions) bool {
	for _, opt := range options {
		if opt.Command == cliv1.Command_COMMAND_BAKE {
			return true
		}
	}
	return false
}

type pull struct {
	imageName   string
	pullOptions load.PullOptions
}

// Collect the build pull option.
// Preconditions: !isBake(msg.Options) && isSavedBuild(msg.Options)
func buildPullOpt(msg *cliv1.GetPullInfoResponse, userTags []string, platform, progress string) *pull {
	// If the user does not specify pull tag names, we use the tags in the build file.
	tags := userTags
	if len(tags) == 0 && len(msg.Options) > 0 && len(msg.Options[0].Tags) > 0 {
		tags = msg.Options[0].Tags
	}

	opts := load.PullOptions{
		UserTags:  tags,
		Quiet:     progress == prog.PrinterModeQuiet,
		KeepImage: true,
		Username:  &msg.Username,
		Password:  &msg.Password,
	}
	if platform != "" {
		opts.Platform = &platform
	}

	return &pull{
		imageName:   msg.Reference,
		pullOptions: opts,
	}
}

func validateTargets(targets []string, msg *cliv1.GetPullInfoResponse) error {
	var validTargets []string
	for _, opt := range msg.Options {
		validTargets = append(validTargets, *opt.TargetName)
	}
	for _, target := range targets {
		if !slices.Contains(validTargets, target) {
			return fmt.Errorf("target %s not found. The available targets are %s", target, strings.Join(validTargets, ", "))
		}
	}
	return nil
}

// Collect all the bake targets to pull.
// Preconditions: isBake(msg.Options) && isSavedBuild(msg.Options) && validateTargets(targets, msg) == nil
func bakePullOpts(msg *cliv1.GetPullInfoResponse, targets, userTags []string, platform, progress string) []*pull {
	pulls := []*pull{}
	for _, opt := range msg.Options {
		// Bake builds always have a target name.
		targetName := *opt.TargetName
		if len(targets) > 0 && !slices.Contains(targets, targetName) {
			continue
		}

		imageName := fmt.Sprintf("%s-%s", msg.Reference, targetName)

		// If a user specified tags, we override the tags in the bake file
		// with <TAG>-<TARGET_NAME>.
		tags := opt.Tags
		if len(userTags) > 0 {
			tags = make([]string, len(userTags))
			for i, tag := range userTags {
				tags[i] = fmt.Sprintf("%s-%s", tag, targetName)
			}
		}

		opts := load.PullOptions{
			UserTags:  tags,
			Quiet:     progress == prog.PrinterModeQuiet,
			KeepImage: true,
			Username:  &msg.Username,
			Password:  &msg.Password,
		}
		if platform != "" {
			opts.Platform = &platform
		}

		pulls = append(pulls, &pull{
			imageName:   imageName,
			pullOptions: opts,
		})
	}

	return pulls
}

func buildPrinter(ctx context.Context, p *pull, progress string) (printer *Printer, cancel context.CancelFunc, err error) {
	displayPhrase := fmt.Sprintf("Pulling image %s", p.imageName)
	printerCtx, cancel := context.WithCancel(ctx)
	printer, err = NewPrinter(printerCtx, displayPhrase, os.Stderr, os.Stderr, progress)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return printer, cancel, nil
}

func bakePrinter(ctx context.Context, ps []*pull, progress string) (printer *Printer, cancel context.CancelFunc, err error) {
	images := []string{}
	for _, p := range ps {
		images = append(images, p.imageName)
	}

	var displayPhrase string
	if len(images) == 1 {
		displayPhrase = fmt.Sprintf("Pulling image %s", images[0])
	} else {
		displayPhrase = fmt.Sprintf("Pulling images %s", strings.Join(images, ", "))
	}
	printerCtx, cancel := context.WithCancel(ctx)
	printer, err = NewPrinter(printerCtx, displayPhrase, os.Stderr, os.Stderr, progress)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return printer, cancel, nil
}
