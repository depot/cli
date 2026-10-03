package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newCmdSessionAction() *cobra.Command {
	var (
		auth   authFlags
		value  string
		fields []string
		output string
	)

	cmd := &cobra.Command{
		Use:   "action [flags] <session-id> <plugin>/<view> <action-or-key>",
		Short: "Run a control on a plugin view",
		Long: `Run a button, select, or form on a plugin view, as if it were clicked in the Depot app.

The control is named by its action, or by the key its chip shows in watch output, such as 1 or a.
A select needs --value; a form takes --field name=value for each input.`,
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
			req := actionRequest{sessionID: args[0], plugin: plugin, viewID: viewID, name: args[2], fields: fields}
			if cmd.Flags().Changed("value") {
				req.value = &value
			}
			resp, err := invokeViewAction(ctx, s, req)
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
}

func invokeViewAction(ctx context.Context, s *session, req actionRequest) (*agentv1.InvokeViewActionResponse, error) {
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
		form, err := formValues(c.Inputs, req.fields)
		if err != nil {
			return nil, err
		}
		call.FormJson = ptr(form)
		call.Value = c.Value
	case "select":
		v, err := selectValue(c, req.value)
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
				if chipTakesValue(c, *value) {
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

func selectValue(c chip, value *string) (string, error) {
	if value != nil {
		return *value, nil
	}
	var opts []string
	for _, o := range c.Options {
		opts = append(opts, o.Value)
	}
	return "", fmt.Errorf("%s is a select; pass --value (options: %s)", c.Action, strings.Join(opts, ", "))
}

func formValues(inputs []formInput, fields []string) (string, error) {
	given := map[string]string{}
	for _, f := range fields {
		name, v, ok := strings.Cut(f, "=")
		if !ok {
			return "", fmt.Errorf("--field must be name=value, got %q", f)
		}
		given[name] = v
	}
	kinds := map[string]string{}
	for _, in := range inputs {
		kinds[in.Name] = in.Kind
		if _, ok := given[in.Name]; !ok && in.Required {
			return "", fmt.Errorf("form field %s is required; pass --field %s=...", in.Name, in.Name)
		}
	}
	form := map[string]any{}
	for name, v := range given {
		if kinds[name] != "checkbox" {
			form[name] = v
			continue
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf("form field %s is a checkbox; use true or false", name)
		}
		form[name] = b
	}
	out, err := json.Marshal(form)
	if err != nil {
		return "", fmt.Errorf("encode form: %w", err)
	}
	return string(out), nil
}
