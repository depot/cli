package dagger

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func NewCmdList() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "dagger [command]",
		Short:                 "Run Dagger pipelines in Depot",
		DisableFlagParsing:    true,
		DisableFlagsInUseLine: true,
		DisableSuggestions:    true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if arg == "--help" || arg == "-h" {
					return help()
				}
			}
			return run(cmd.Context(), args)
		},
	}

	flags := cmd.Flags()
	_ = flags

	return cmd
}

func run(ctx context.Context, args []string) error {
	path, err := exec.LookPath("dagger")
	if err != nil {
		return err
	}

	output, err := exec.Command(path, "version").Output()
	if err != nil {
		return err
	}
	parsed := strings.Split(string(output), " ")
	if len(parsed) < 2 {
		return fmt.Errorf("unable able to parse dagger version")
	}
	version := parsed[1]

	// TODO: Send to API.
	_ = version

	cmd := exec.Command(path, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	io.Copy(os.Stderr, stderr)
	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}

func help() error {
	path, err := exec.LookPath("dagger")
	if err != nil {
		return err
	}

	output, err := exec.Command(path, "help").Output()
	if err != nil {
		return err
	}

	help := strings.Replace(string(output), "  dagger", "depot dagger", -1)
	help = strings.Replace(help, "Flags:", "Flags:\n      --project string      Depot project ID\n      --token string        Depot token\n      --platform string     Run builds on this platform (\"dynamic\", \"linux/amd64\", \"linux/arm64\") (default \"dynamic\")\n", -1)
	fmt.Printf("%s\n", help)

	return nil
}
