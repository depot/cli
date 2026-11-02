package registry

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type options struct{ token, output string }

func NewCmdRegistry() *cobra.Command { return newCmdRegistry(run) }

func newCmdRegistry(proxy func() error) *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use: "registry", Short: "Manage container registry repositories, images, and policies", Args: cobra.NoArgs,
		Long: `Manage the organization container registry. Requires an organization API token
via --token or DEPOT_TOKEN. The token selects the organization. Commands return
JSON; list commands return one page with pagination metadata.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Released proxy images and older clients invoke `depot registry` directly.
			// Only the complete internal configuration selects that legacy mode.
			for _, key := range []string{"CA_CERT", "KEY", "CERT", "ADDR", "SERVER_NAME", "MANIFEST", "CONFIG"} {
				if os.Getenv(key) == "" {
					return cmd.Help()
				}
			}
			return proxy()
		},
	}
	cmd.PersistentFlags().StringVar(&o.token, "token", "", "Organization API token (or DEPOT_TOKEN)")
	cmd.PersistentFlags().StringVarP(&o.output, "output", "o", "json", "Output format (json)")
	cmd.AddCommand(repositoryCommands(o), imageCommands(o), tagCommands(o), tokenCommands(o), retentionCommands(o), upstreamCommands(o))
	return cmd
}

func command(use, short string, nargs int, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(nargs)(cmd, args); err != nil {
			return err
		}
		for _, arg := range args {
			if strings.TrimSpace(arg) == "" {
				return fmt.Errorf("arguments must not be empty")
			}
		}
		return nil
	}, RunE: run}
}
func confirm(cmd *cobra.Command) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		return fmt.Errorf("this operation requires --yes")
	}
	return nil
}
func destructive(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().BoolP("yes", "y", false, "Confirm this destructive operation")
	return cmd
}
func (o *options) auth() (string, error) {
	if o.output != "json" {
		return "", fmt.Errorf("unsupported output format (use json)")
	}
	token := o.token
	if token == "" {
		token = os.Getenv("DEPOT_TOKEN")
	}
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("an organization API token is required (--token or DEPOT_TOKEN)")
	}
	return token, nil
}
func call[Req any, Resp any](cmd *cobra.Command, o *options, request *Req, rpc func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error)) (*Resp, error) {
	token, err := o.auth()
	if err != nil {
		return nil, err
	}
	response, err := rpc(cmd.Context(), api.WithAuthentication(connect.NewRequest(request), token))
	if err != nil {
		// Do not echo server messages: upstream requests can contain credentials.
		return nil, fmt.Errorf("registry operation failed (%s)", connect.CodeOf(err))
	}
	return response.Msg, nil
}
func write(cmd *cobra.Command, msg proto.Message) error {
	data, err := (protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: true}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

type pagination struct {
	page, size int32
	query      string
}

func (p *pagination) flags(cmd *cobra.Command) {
	cmd.Flags().Int32Var(&p.page, "page", 1, "Page number (starts at 1)")
	cmd.Flags().Int32Var(&p.size, "page-size", 25, "Results per page (1-100)")
	cmd.Flags().StringVar(&p.query, "query", "", "Filter by name")
}
func (p *pagination) validate() error {
	if p.page < 1 || p.size < 1 || p.size > 100 {
		return fmt.Errorf("--page must be positive and --page-size must be 1-100")
	}
	return nil
}
func (p *pagination) filter(cmd *cobra.Command) *string {
	if cmd.Flags().Changed("query") {
		return &p.query
	}
	return nil
}

var repositorySegment = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

func validateRepository(value string, patterns bool) error {
	if len(value) == 0 || len(value) > 255 {
		return fmt.Errorf("repository must contain 1-255 characters")
	}
	for _, segment := range strings.Split(value, "/") {
		if patterns && (segment == "*" || segment == "**") {
			continue
		}
		if !repositorySegment.MatchString(segment) {
			if patterns {
				return fmt.Errorf("use a repository path such as team/app or a whole-segment wildcard")
			}
			return fmt.Errorf("use a repository path such as team/app (without host, tag, or digest)")
		}
	}
	return nil
}
func password(cmd *cobra.Command) (string, error) {
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
	if err != nil {
		return "", fmt.Errorf("could not read password from stdin")
	}
	if len(data) > 65536 {
		return "", fmt.Errorf("password exceeds 64 KiB")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
}

// Keep credentials out of URL arguments and response metadata. The API decides
// which upstream destinations are supported.
func validateUpstreamURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--url must not be empty")
	}
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("--url must be an HTTP(S) registry URL or hostname")
	}
	if parsed.User != nil {
		return fmt.Errorf("do not include credentials in --url; use --username and --password-stdin")
	}
	return nil
}
