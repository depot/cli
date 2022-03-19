package version

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func NewCmdVersion(version, buildDate string) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "version",
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(Format(version, buildDate))
		},
	}
	return cmd
}

func Format(version, buildDate string) string {
	version = strings.TrimPrefix(version, "v")
	if buildDate != "" {
		buildDate = fmt.Sprintf(" (%s)", buildDate)
	}
	return fmt.Sprintf("depot version %s%s\n%s\n", version, buildDate, changelogURL(version))
}

func changelogURL(version string) string {
	path := "https://github.com/depot/cli"
	r := regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[\w.]+)?$`)
	if !r.MatchString(version) {
		return fmt.Sprintf("%s/releases/latest", path)
	}
	url := fmt.Sprintf("%s/releases/tag/v%s", path, strings.TrimPrefix(version, "v"))
	return url
}
