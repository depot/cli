package checks

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/spf13/cobra"
)

func newAgents(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use: "agents", Short: "Manage saved review agent configurations",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newAgentList(o), newAgentGet(o), newAgentSave(o, false), newAgentSave(o, true), newAgentModels(o))
	return cmd
}

func newAgentList(o *options) *cobra.Command {
	p := &pagination{}
	cmd := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List saved review agents", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := p.validate(); err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			client := api.NewReviewAgentClient()
			result := &checksv1.ListReviewAgentsResponse{}
			for {
				resp, err := client.ListReviewAgents(cmd.Context(), request(o, &checksv1.ListReviewAgentsRequest{PageSize: p.pageSize(len(result.Agents)), PageToken: p.token}))
				if err != nil {
					return fmt.Errorf("failed to list review agents: %w", err)
				}
				result.Agents = append(result.Agents, resp.Msg.Agents...)
				if len(result.Agents) > p.n {
					result.Agents = result.Agents[:p.n]
				}
				next, err := p.next(resp.Msg.NextPageToken, len(result.Agents))
				if err != nil {
					return err
				}
				if !next {
					break
				}
			}
			if o.output == "json" {
				agents := make([]*agentJSON, 0, len(result.Agents))
				for _, agent := range result.Agents {
					agents = append(agents, agentToJSON(agent))
				}
				return o.writeJSON(cmd, agents)
			}
			return printAgents(cmd, result.Agents, true)
		},
	}
	p.flags(cmd)
	return cmd
}

func newAgentGet(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "get <agent-id>", Aliases: []string{"show"}, Short: "Show saved review agent configuration", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewReviewAgentClient().GetReviewAgent(cmd.Context(), request(o, &checksv1.GetReviewAgentRequest{AgentId: args[0]}))
			if err != nil {
				return fmt.Errorf("failed to get review agent: %w", err)
			}
			if o.output == "json" {
				return o.writeJSON(cmd, agentToJSON(resp.Msg.Agent))
			}
			return printAgent(cmd, resp.Msg.Agent)
		},
	}
}

func newAgentModels(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "models", Short: "List available review models and the current default", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewReviewAgentClient().ListReviewAgentModels(cmd.Context(), request(o, &checksv1.ListReviewAgentModelsRequest{}))
			if err != nil {
				return fmt.Errorf("failed to list review agent models: %w", err)
			}
			if o.output == "json" {
				return o.writeJSON(cmd, resp.Msg)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Default: %s\n\n", resp.Msg.DefaultModelId)
			for _, model := range resp.Msg.Models {
				fmt.Fprintf(&b, "%s\t%s\n", model.Id, model.Label)
			}
			_, err = io.WriteString(cmd.OutOrStdout(), b.String())
			return err
		},
	}
}

func newAgentSave(o *options, update bool) *cobra.Command {
	var name, instructions, file, model string
	var repos, events []string
	var clearRepos, clearEvents bool
	use, short := "create", "Create a review agent"
	args := cobra.NoArgs
	if update {
		use, short, args = "update <agent-id>", "Update selected review agent configuration", cobra.ExactArgs(1)
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Args: args,
		Long:    short + ". Omitted update fields are preserved. Supplied repositories/events replace the saved selection.\nProvider-qualified events apply to every watched repository of that provider. On create, omitted events enable supported events except github:push.",
		Example: "  depot checks agents create --name Security --instructions-file review.md --repo github:depot/api\n  depot checks agents update <agent-id> --model default\n  depot checks agents update <agent-id> --clear-repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			changed := cmd.Flags().Changed
			if !update && (!changed("name") || (!changed("instructions") && !changed("instructions-file"))) {
				return fmt.Errorf("create requires --name and either --instructions or --instructions-file")
			}
			if update && !changed("name") && !changed("instructions") && !changed("instructions-file") && !changed("model") && !changed("repo") && !clearRepos && !changed("event") && !clearEvents {
				return fmt.Errorf("supply at least one configuration change")
			}
			if changed("name") && (strings.TrimSpace(name) == "" || len([]rune(name)) > 191) {
				return fmt.Errorf("name must contain 1-191 characters")
			}
			if changed("instructions-file") {
				var reader io.Reader = cmd.InOrStdin()
				if file != "-" {
					f, err := os.Open(file)
					if err != nil {
						return err
					}
					defer f.Close()
					reader = f
				}
				data, err := io.ReadAll(io.LimitReader(reader, 60001))
				if err != nil {
					return err
				}
				instructions = string(data)
			}
			if changed("instructions") || changed("instructions-file") {
				if strings.TrimSpace(instructions) == "" || len(instructions) > 60000 {
					return fmt.Errorf("instructions must contain 1-60000 UTF-8 bytes")
				}
			}
			var selection *checksv1.RepositorySelection
			if changed("repo") || clearRepos {
				selection = &checksv1.RepositorySelection{}
				for _, value := range repos {
					repo, err := parseRepo(value)
					if err != nil {
						return err
					}
					selection.Items = append(selection.Items, repo)
				}
				if len(selection.Items) > 100 {
					return fmt.Errorf("at most 100 repositories can be watched")
				}
			}
			var triggers *checksv1.ReviewAgentTriggers
			if changed("event") || clearEvents {
				for _, event := range events {
					if strings.TrimSpace(event) == "" {
						return fmt.Errorf("event cannot be empty; use --clear-events to disable automatic checks")
					}
				}
				triggers = &checksv1.ReviewAgentTriggers{Events: events}
			}
			if model == "default" {
				model = ""
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			client := api.NewReviewAgentClient()
			var agent *checksv1.ReviewAgent
			if update {
				req := &checksv1.UpdateReviewAgentRequest{AgentId: args[0], Repos: selection, Triggers: triggers}
				if changed("name") {
					req.Name = &name
				}
				if changed("instructions") || changed("instructions-file") {
					req.Instructions = &instructions
				}
				if changed("model") {
					req.ModelId = &model
				}
				resp, err := client.UpdateReviewAgent(cmd.Context(), request(o, req))
				if err != nil {
					return fmt.Errorf("failed to update review agent: %w", err)
				}
				agent = resp.Msg.Agent
			} else {
				resp, err := client.CreateReviewAgent(cmd.Context(), request(o, &checksv1.CreateReviewAgentRequest{Name: name, Instructions: instructions, ModelId: model, Repos: selection, Triggers: triggers}))
				if err != nil {
					return fmt.Errorf("failed to create review agent: %w", err)
				}
				agent = resp.Msg.Agent
			}
			if o.output == "json" {
				return o.writeJSON(cmd, agentToJSON(agent))
			}
			return printAgent(cmd, agent)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Review agent name")
	cmd.Flags().StringVar(&instructions, "instructions", "", "Instructions for future checks")
	cmd.Flags().StringVar(&file, "instructions-file", "", "Read instructions from this file (- for stdin)")
	cmd.Flags().StringVar(&model, "model", "", "Model ID from `checks agents models`, or default")
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "Watched <forge>:<repository> (repeatable; replaces the selection)")
	cmd.Flags().StringArrayVar(&events, "event", nil, "Provider-qualified trigger (repeatable, e.g. github:opened; replaces the selection)")
	cmd.Flags().BoolVar(&clearRepos, "clear-repos", false, "Stop watching all repositories")
	cmd.Flags().BoolVar(&clearEvents, "clear-events", false, "Disable automatic checks")
	cmd.MarkFlagsMutuallyExclusive("instructions", "instructions-file")
	cmd.MarkFlagsMutuallyExclusive("repo", "clear-repos")
	cmd.MarkFlagsMutuallyExclusive("event", "clear-events")
	return cmd
}
