package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newCmdSessionAction() *cobra.Command {
	var (
		auth   authFlags
		value  string
		fields []string
		yes    bool
		output string
	)

	cmd := &cobra.Command{
		Use:   "action [flags] <session-id> <plugin>/<view> <action-or-key>",
		Short: "Run a control on a plugin view",
		Long: `Run a button, select, or form on a plugin view, as if it were clicked in the Depot app.

The control is named by its action, or by the key its chip shows in watch output, such as 1 or a.
A select needs --value; a form takes --field name=value for each input.
Missing values are prompted for when stdin is a terminal.`,
		Example: `  # Press the chip shown as [1] on the todos plugin's plan view
  depot agent session action <session-id> todos/plan 1

  # Submit a form
  depot agent session action <session-id> todos/plan add --field text="Write docs"`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			plugin, viewID, ok := strings.Cut(args[1], "/")
			if !ok || plugin == "" || viewID == "" {
				return fmt.Errorf("view must be <plugin>/<view>, got %q", args[1])
			}
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			req := actionRequest{sessionID: args[0], plugin: plugin, viewID: viewID, name: args[2], fields: fields, yes: yes}
			if cmd.Flags().Changed("value") {
				req.value = &value
			}
			p := newPrompter(os.Stdin, os.Stderr, term.IsTerminal(int(os.Stdin.Fd())))
			resp, err := invokeViewAction(ctx, s, req, p)
			if errors.Is(err, errNotConfirmed) {
				fmt.Fprintln(os.Stderr, "Not confirmed; nothing was sent.")
				return nil
			}
			if err != nil {
				return err
			}
			if output == "json" {
				return writeProtoJSON(resp)
			}
			fmt.Printf("Queued %s as input %s\n", safeText(args[1]+" "+args[2]), safeText(resp.GetInput().GetInputId()))
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVar(&value, "value", "", "Value for the action, such as a select option")
	cmd.Flags().StringArrayVar(&fields, "field", nil, "Form field as name=value (repeatable)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

type actionRequest struct {
	sessionID string
	plugin    string
	viewID    string
	name      string
	value     *string
	fields    []string
	yes       bool
}

var errNotConfirmed = errors.New("not confirmed")

// prompter asks for missing values and confirmations, but only on an interactive stdin.
type prompter struct {
	in          *bufio.Reader
	out         io.Writer
	interactive bool
}

func newPrompter(in io.Reader, out io.Writer, interactive bool) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out, interactive: interactive}
}

func (p *prompter) ask(question string) (string, error) {
	fmt.Fprint(p.out, safeText(question))
	line, err := p.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func invokeViewAction(ctx context.Context, s *session, req actionRequest, p *prompter) (*agentv1.InvokeViewActionResponse, error) {
	got, err := withRetries(ctx, func() (*connect.Response[agentv1.GetSessionResponse], error) {
		return s.client.GetSession(ctx, authed(s, &agentv1.GetSessionRequest{SessionId: req.sessionID, Client: cliViewClient()}))
	})
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", req.sessionID, err)
	}
	views, err := parsePluginViews(got.Msg.GetViewsJson())
	if err != nil {
		return nil, err
	}
	var view *pluginView
	var refs []string
	for i := range views.Views {
		v := views.Views[i]
		refs = append(refs, v.ref())
		if v.Plugin == req.plugin && v.ID == req.viewID {
			view = &v
		}
	}
	if view == nil {
		if len(refs) == 0 {
			return nil, fmt.Errorf("session %s has no plugin views", req.sessionID)
		}
		return nil, fmt.Errorf("session %s has no view %s/%s (views: %s)", req.sessionID, req.plugin, req.viewID, strings.Join(refs, ", "))
	}
	_, chips := layoutView(*view, 80)
	c, err := pickChip(chips, req.name, req.value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", view.ref(), err)
	}

	call := &agentv1.InvokeViewActionRequest{
		SessionId:       req.sessionID,
		Plugin:          view.Plugin,
		ViewId:          view.ID,
		Action:          c.Action,
		ClientRequestId: ptr(uuid.NewString()),
	}
	if c.Kind != "form" && len(req.fields) > 0 {
		return nil, fmt.Errorf("--field is only for a form; %s is a %s", c.Action, c.Kind)
	}
	switch c.Kind {
	case "form":
		form, err := formValues(c.Inputs, req.fields, p)
		if err != nil {
			return nil, err
		}
		call.FormJson = ptr(form)
	case "select":
		v, err := selectValue(c, req.value, p)
		if err != nil {
			return nil, err
		}
		call.Value = ptr(v)
	default:
		call.Value = c.Value
		if req.value != nil {
			call.Value = req.value
		}
	}
	if c.needsConfirm() && !req.yes {
		if err := confirmAction(c, p); err != nil {
			return nil, err
		}
	}

	resp, err := withRetries(ctx, func() (*connect.Response[agentv1.InvokeViewActionResponse], error) {
		return s.client.InvokeViewAction(ctx, authed(s, call))
	})
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w", view.ref(), c.Action, err)
	}
	return resp.Msg, nil
}

// pickChip resolves an action name first, then a chip key.
// Several controls for one action, such as a "complete" button per item, need a value to tell them apart.
func pickChip(chips []chip, name string, value *string) (chip, error) {
	var byAction []chip
	for _, c := range chips {
		if c.Action == name {
			byAction = append(byAction, c)
		}
	}
	if len(byAction) > 0 {
		if value != nil {
			for _, c := range byAction {
				if chipTakesValue(c, *value) || c.Kind == "form" {
					return c, nil
				}
			}
			return byAction[0], nil
		}
		var values []string
		for _, c := range byAction {
			if c.Value != nil && !slices.Contains(values, *c.Value) {
				values = append(values, *c.Value)
			}
		}
		if len(values) > 1 {
			return chip{}, fmt.Errorf("action %s needs --value (one of %s)", name, strings.Join(values, ", "))
		}
		return byAction[0], nil
	}
	for _, c := range chips {
		if c.Key == name {
			return c, nil
		}
	}
	var avail []string
	for _, c := range chips {
		avail = append(avail, fmt.Sprintf("[%s] %s", c.Key, c.Action))
	}
	if len(avail) == 0 {
		return chip{}, fmt.Errorf("view has no controls")
	}
	return chip{}, fmt.Errorf("no action or key %q (controls: %s)", name, strings.Join(avail, ", "))
}

func selectValue(c chip, value *string, p *prompter) (string, error) {
	if value == nil {
		if !p.interactive {
			return "", fmt.Errorf("%s is a select; pass --value", c.Action)
		}
		for i, o := range c.Options {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, span(o.Label, plainStyle).text)
		}
		answer, err := p.ask(c.Label + ": ")
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(strings.TrimSpace(answer)); err == nil && n >= 1 && n <= len(c.Options) {
			return c.Options[n-1].Value, nil
		}
		value = &answer
	}
	for _, o := range c.Options {
		if o.Value == *value {
			return o.Value, nil
		}
	}
	var opts []string
	for _, o := range c.Options {
		opts = append(opts, o.Value)
	}
	return "", fmt.Errorf("%q is not an option for %s (options: %s)", *value, c.Action, strings.Join(opts, ", "))
}

func formValues(inputs []formInput, fields []string, p *prompter) (string, error) {
	given := map[string]string{}
	for _, f := range fields {
		name, v, ok := strings.Cut(f, "=")
		if !ok {
			return "", fmt.Errorf("--field must be name=value, got %q", f)
		}
		if !slices.ContainsFunc(inputs, func(in formInput) bool { return in.Name == name }) {
			return "", fmt.Errorf("form has no field %q", name)
		}
		given[name] = v
	}
	form := map[string]any{}
	for _, in := range inputs {
		v, ok := given[in.Name]
		if !ok && in.Required {
			if !p.interactive {
				return "", fmt.Errorf("form field %s is required; pass --field %s=...", in.Name, in.Name)
			}
			label := in.Label
			if label == "" {
				label = in.Name
			}
			answer, err := p.ask(label + ": ")
			if err != nil {
				return "", err
			}
			v, ok = answer, true
		}
		if !ok {
			continue
		}
		switch in.Kind {
		case "checkbox":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return "", fmt.Errorf("form field %s is a checkbox; use true or false", in.Name)
			}
			form[in.Name] = b
			continue
		case "select":
			if !slices.ContainsFunc(in.Options, func(o selectOption) bool { return o.Value == v }) {
				return "", fmt.Errorf("%q is not an option for form field %s", v, in.Name)
			}
		}
		if in.MaxLength > 0 && len([]rune(v)) > in.MaxLength {
			return "", fmt.Errorf("form field %s is longer than %d characters", in.Name, in.MaxLength)
		}
		form[in.Name] = v
	}
	out, err := json.Marshal(form)
	if err != nil {
		return "", fmt.Errorf("encode form: %w", err)
	}
	return string(out), nil
}

func confirmAction(c chip, p *prompter) error {
	if !p.interactive {
		return fmt.Errorf("%s asks for confirmation; pass --yes to run it", c.Action)
	}
	question := "Run " + c.Label + "?"
	if c.Confirm != nil {
		question = c.Confirm.Title
		if c.Confirm.Text != "" {
			question += " " + c.Confirm.Text
		}
	}
	answer, err := p.ask(span(question, plainStyle).text + " [y/N] ")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errNotConfirmed
}
