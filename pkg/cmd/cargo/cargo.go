package cargo

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

var orgID string

func NewCmdCargo() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "cargo [cargo-flags]...",
		Short:              "Run cargo with Depot caching",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Manual flag parsing to handle --org
			var cargoArgs []string
			i := 0
			for i < len(args) {
				arg := args[i]
				if arg == "--org" && i+1 < len(args) {
					orgID = args[i+1]
					i += 2
				} else if after, ok := strings.CutPrefix(arg, "--org="); ok {
					orgID = after
					i++
				} else {
					cargoArgs = append(cargoArgs, arg)
					i++
				}
			}

			// If no org ID specified via flag, check environment variable
			if orgID == "" {
				orgID = os.Getenv("DEPOT_ORG_ID")
			}

			// Check for sccache
			sccachePath, err := exec.LookPath("sccache")
			if err != nil {
				return fmt.Errorf("sccache not found in PATH: %w\n\nPlease install sccache: cargo install sccache", err)
			}

			// Get authentication token
			token, err := helpers.ResolveToken(ctx, "")
			if err != nil {
				return fmt.Errorf("failed to resolve token: %w", err)
			}

			// Create cargo command
			cargoCmd := exec.CommandContext(ctx, "cargo", cargoArgs...)
			cargoCmd.Stdin = os.Stdin
			cargoCmd.Stdout = os.Stdout
			cargoCmd.Stderr = os.Stderr

			// Inherit environment and filter out existing sccache vars
			cargoCmd.Env = []string{}
			for _, env := range os.Environ() {
				// Skip any existing SCCACHE environment variables
				if !strings.HasPrefix(env, "SCCACHE_") {
					cargoCmd.Env = append(cargoCmd.Env, env)
				}
			}

			// Now add our sccache vars
			cargoCmd.Env = append(cargoCmd.Env, fmt.Sprintf("RUSTC_WRAPPER=%s", sccachePath))
			cargoCmd.Env = append(cargoCmd.Env, "SCCACHE_WEBDAV_ENDPOINT=https://cache.depot.dev")

			if orgID != "" {
				// Use org-specific authentication
				cargoCmd.Env = append(cargoCmd.Env, fmt.Sprintf("SCCACHE_WEBDAV_USERNAME=%s", orgID))
				cargoCmd.Env = append(cargoCmd.Env, fmt.Sprintf("SCCACHE_WEBDAV_PASSWORD=%s", token))
			} else {
				// Use token-only authentication
				cargoCmd.Env = append(cargoCmd.Env, fmt.Sprintf("SCCACHE_WEBDAV_TOKEN=%s", token))
			}

			// Set up signal handling
			signalCh := make(chan os.Signal, 1)
			signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-signalCh
				if cargoCmd.Process != nil {
					cargoCmd.Process.Signal(sig)
				}
			}()

			// Run cargo
			err = cargoCmd.Run()
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					os.Exit(exitErr.ExitCode())
				}
				return err
			}

			return nil
		},
	}

	return cmd
}

func init() {
	cargoCmd := NewCmdCargo()
	// Add a hidden --org flag for command completion, but we parse it manually
	cargoCmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cargoCmd.Flags().Lookup("org").Hidden = true
}
