// Package arguments validates the number of positional arguments of a
// command. The error messages match docker/cli v24, which earlier versions
// of the Depot CLI used.
package arguments

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Exact returns an error if the command does not have number arguments.
func Exact(number int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == number {
			return nil
		}
		return usageError(cmd, "requires exactly %d %s", number)
	}
}

// AtLeast returns an error if the command has fewer than min arguments.
func AtLeast(min int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min {
			return nil
		}
		return usageError(cmd, "requires at least %d %s", min)
	}
}

// AtMost returns an error if the command has more than max arguments.
func AtMost(max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= max {
			return nil
		}
		return usageError(cmd, "requires at most %d %s", max)
	}
}

func usageError(cmd *cobra.Command, requirement string, number int) error {
	noun := "argument"
	if number != 1 {
		noun = "arguments"
	}
	return errors.Errorf(
		"%q "+requirement+".\nSee '%s --help'.\n\nUsage:  %s\n\n%s",
		cmd.CommandPath(),
		number,
		noun,
		cmd.CommandPath(),
		cmd.UseLine(),
		cmd.Short,
	)
}
