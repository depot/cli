package ci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

var (
	sourceSlugPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,58}$`)
	sourceKeyPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sourceBodyReference  = regexp.MustCompile(`^request\.body(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	sourceWholeReference = regexp.MustCompile(`^\$\{\{\s*(request\.body(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\s*\}\}$`)
)

func NewCmdSource() *cobra.Command {
	cmd := &cobra.Command{
		Use: "source", Short: "Manage CI event source credentials",
		Long: `Create, list, rotate, revoke, or recatch CI event sources.

Creating a source issues a credential only; it does not activate a mapping. Commit the source YAML in Depot Code before compiling an event wait. A webhook URL is a credential: never commit it, log it, or share it in a ticket.`,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newCmdSourceCreate(), newCmdSourceList(), newCmdSourceRotate(), newCmdSourceRevoke(), newCmdSourceRecatch())
	return cmd
}

func newCmdSourceCreate() *cobra.Command {
	var flags m1Flags
	var repo, slug, event, state, delivery string
	var keys, fields []string
	cmd := &cobra.Command{
		Use: "create", Short: "Issue a source credential and print commit-ready YAML",
		Long: `Create a credential and a safe, inert source declaration. References must be whole-value request.body paths; expressions and interpolation are not supported.

By default, stdout contains only YAML and stderr prints the secret webhook URL once, so redirecting stdout to a file does not save the credential. Explicit --output json includes the credential URL and must not be redirected into a repository. Creation is not activation: commit the source file before compiling a wait.`,
		Example: `  # Save the declaration; the credential URL goes to stderr only
  depot ci source create --repo chains --slug planet --event planetscale.deploy_request \
    --key database=request.body.database --key number=request.body.resource.number \
    --state request.body.event --field branch=request.body.resource.branch \
    --delivery-id request.body.id > .depot/sources/planet.yml

  # Retrieve the complete response, including the credential (do not commit)
  depot ci source create --repo chains --slug planet --event planetscale.deploy_request \
    --key database=request.body.database --state request.body.event --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(repo) == "" || len(repo) > 512 {
				return fmt.Errorf("--repo must be nonempty and at most 512 bytes")
			}
			definition, err := buildSourceDefinition(slug, event, keys, state, fields, delivery)
			if err != nil {
				return err
			}
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			resp, err := api.CICreateEventSource(cmd.Context(), token, org, api.CICreateEventSourceRequest{Repo: repo, Slug: slug, Definition: definition})
			if err != nil {
				return err
			}
			// Print the locally validated declaration rather than untrusted server
			// content. The API response must return that exact credential-free file.
			if resp.SourceFile.Path != ".depot/sources/"+slug+".yml" || resp.SourceFile.Content != definition {
				return fmt.Errorf("server returned a different source declaration; no file was printed")
			}
			if outputIsJSON(flags.output) {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Warning: JSON output includes a credential URL; do not commit or log it"); err != nil {
					return err
				}
				return m1WriteJSON(cmd, resp.Raw)
			}
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Webhook URL (credential; save securely, never commit): "+resp.URL); err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), definition)
			return err
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&repo, "repo", "", "Depot Code repository (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "Source slug used in .depot/sources/<slug>.yml (required)")
	cmd.Flags().StringVar(&event, "event", "", "Literal event type (required)")
	cmd.Flags().StringArrayVar(&keys, "key", nil, "Correlation key as name=request.body.path (repeatable; required)")
	cmd.Flags().StringVar(&state, "state", "", "State reference as request.body.path (required)")
	cmd.Flags().StringArrayVar(&fields, "field", nil, "Public field as name=request.body.path (repeatable)")
	cmd.Flags().StringVar(&delivery, "delivery-id", "", "Optional delivery ID reference as request.body.path")
	for _, flag := range []string{"repo", "slug", "event", "key", "state"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func newCmdSourceList() *cobra.Command {
	var flags m1Flags
	var repo string
	cmd := &cobra.Command{
		Use: "list", Short: "List event sources in a repository", Args: cobra.NoArgs,
		Long:    "List server-authoritative source metadata. Issued secret URLs are not returned by the list endpoint.",
		Example: "  # List sources\n  depot ci source list --repo chains\n\n  # Print JSON\n  depot ci source list --repo chains --output json",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			resp, err := api.CIListEventSources(cmd.Context(), token, org, repo)
			if err != nil {
				return err
			}
			return m1WriteJSON(cmd, resp)
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&repo, "repo", "", "Depot Code repository (required)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
func newCmdSourceRotate() *cobra.Command {
	var flags m1Flags
	cmd := &cobra.Command{
		Use: "rotate <source-id>", Short: "Rotate an event source credential", Args: cobra.ExactArgs(1),
		Long:    "Invalidate the previous source credential and issue a new one. Default output prints the secret URL once to stderr only. Explicit JSON output includes a credential URL; never commit or log it.",
		Example: "  # Replace a credential\n  depot ci source rotate <source-id>\n\n  # Retrieve credential JSON securely (do not commit)\n  depot ci source rotate <source-id> --output json",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			resp, err := api.CIRotateEventSource(cmd.Context(), token, org, args[0])
			if err != nil {
				return err
			}
			if outputIsJSON(flags.output) {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Warning: JSON output includes a credential URL; do not commit or log it"); err != nil {
					return err
				}
				return m1WriteJSON(cmd, resp.Raw)
			}
			_, err = fmt.Fprintln(cmd.ErrOrStderr(), "Webhook URL (credential; save securely, never commit): "+resp.URL)
			return err
		},
	}
	flags.add(cmd)
	return cmd
}
func newCmdSourceRevoke() *cobra.Command {
	var flags m1Flags
	cmd := &cobra.Command{
		Use: "revoke <source-id>", Short: "Revoke an event source credential", Args: cobra.ExactArgs(1),
		Long:    "Disable credential-based delivery for this source. Source declarations in Depot Code are not removed.",
		Example: "  # Disable a credential\n  depot ci source revoke <source-id>\n\n  # Inspect the result\n  depot ci source revoke <source-id> --output json",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			resp, err := api.CIRevokeEventSource(cmd.Context(), token, org, args[0])
			if err != nil {
				return err
			}
			if outputIsJSON(flags.output) {
				return m1WriteJSON(cmd, resp)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Source credential revoked")
			return err
		},
	}
	flags.add(cmd)
	return cmd
}
func newCmdSourceRecatch() *cobra.Command {
	var flags m1Flags
	var bodyFile, evidence string
	cmd := &cobra.Command{
		Use: "recatch <source-id>", Short: "Recatch a source event with evidence", Args: cobra.ExactArgs(1),
		Long:    "Submit a bounded JSON body with nonempty evidence. The server authenticates the operator and records trusted provenance; identities claimed in the body are not authoritative.",
		Example: "  # Recatch an event\n  depot ci source recatch <source-id> --body-file event.json --evidence 'Verified upstream delivery'\n\n  # Inspect the server result\n  depot ci source recatch <source-id> --body-file event.json --evidence 'Verified upstream delivery' --output json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(evidence) == "" || len(evidence) > api.CIEventBodyMaxBytes {
				return fmt.Errorf("--evidence must be nonempty and at most 64 KiB")
			}
			body, err := readSourceBody(bodyFile)
			if err != nil {
				return err
			}
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			resp, err := api.CIRecatchEventSource(cmd.Context(), token, org, args[0], api.CIRecatchEventSourceRequest{Body: body, Evidence: evidence})
			if err != nil {
				return err
			}
			return m1WriteJSON(cmd, resp)
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "JSON body file of at most 64 KiB (required)")
	cmd.Flags().StringVar(&evidence, "evidence", "", "Evidence for the recatch (required; server records authenticated operator)")
	_ = cmd.MarkFlagRequired("body-file")
	_ = cmd.MarkFlagRequired("evidence")
	return cmd
}

func readSourceBody(path string) (json.RawMessage, error) {
	// Check before opening: opening a FIFO would block even before Stat.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("--body-file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open --body-file")
	}
	defer file.Close()
	// Reject FIFOs/devices so a file read cannot wait forever before the request.
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("--body-file must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, api.CIEventBodyMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read --body-file")
	}
	if len(body) > api.CIEventBodyMaxBytes || !json.Valid(body) {
		return nil, fmt.Errorf("--body-file must be valid JSON of at most 64 KiB")
	}
	return json.RawMessage(body), nil
}

func buildSourceDefinition(slug, event string, keys []string, state string, fields []string, delivery string) (string, error) {
	if !sourceSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("--slug must be 1-59 lowercase letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	if strings.TrimSpace(event) == "" || len(event) > 100 || strings.Contains(event, "${{") {
		return "", fmt.Errorf("--event must be a nonempty literal of at most 100 bytes")
	}
	keyMap, err := sourceMappings(keys, "key")
	if err != nil {
		return "", err
	}
	if len(keyMap) == 0 || len(keyMap) > 32 {
		return "", fmt.Errorf("provide 1-32 --key components")
	}
	fieldMap, err := sourceMappings(fields, "field")
	if err != nil {
		return "", err
	}
	stateRef, err := sourceReference(state)
	if err != nil {
		return "", fmt.Errorf("--state requires a whole-value request.body reference")
	}
	var deliveryRef string
	if delivery != "" {
		deliveryRef, err = sourceReference(delivery)
		if err != nil {
			return "", fmt.Errorf("--delivery-id requires a whole-value request.body reference")
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "name: %s\nevent: %s\nkey:\n", strconv.Quote(slug), strconv.Quote(event))
	writeSourceMappings(&b, keyMap)
	fmt.Fprintf(&b, "state: %s\n", strconv.Quote(stateRef))
	if deliveryRef != "" {
		fmt.Fprintf(&b, "delivery-id: %s\n", strconv.Quote(deliveryRef))
	}
	if len(fieldMap) == 0 {
		b.WriteString("fields: {}\n")
	} else {
		b.WriteString("fields:\n")
		writeSourceMappings(&b, fieldMap)
	}
	if b.Len() > api.CIEventBodyMaxBytes {
		return "", fmt.Errorf("source declaration exceeds 64 KiB")
	}
	return b.String(), nil
}
func sourceMappings(raw []string, flag string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !sourceKeyPattern.MatchString(key) || len(key) > 200 {
			return nil, fmt.Errorf("invalid --%s: expected name=request.body.path with an identifier name", flag)
		}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate --%s component", flag)
		}
		reference, err := sourceReference(value)
		if err != nil {
			return nil, fmt.Errorf("--%s requires a whole-value request.body reference", flag)
		}
		out[key] = reference
	}
	return out, nil
}
func sourceReference(value string) (string, error) {
	if len(value) > 2000 {
		return "", fmt.Errorf("reference too long")
	}
	if match := sourceWholeReference.FindStringSubmatch(value); match != nil {
		value = match[1]
	}
	if !sourceBodyReference.MatchString(value) {
		return "", fmt.Errorf("invalid reference")
	}
	return "${{ " + value + " }}", nil
}
func writeSourceMappings(out *bytes.Buffer, values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(out, "  %s: %s\n", strconv.Quote(key), strconv.Quote(values[key]))
	}
}
