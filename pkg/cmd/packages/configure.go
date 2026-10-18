package packages

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

const (
	authDefault = ""
	authStore   = "store"
)

type options struct {
	org             string
	repository      string
	auth            string
	replaceUpstream bool
	scope           string
	authOnly        bool
	suite           string
	components      []string
	branch          string
	path            string
	signingKey      string
	packageKey      string
}

type client struct {
	format string
	// upstream names the built-in cache that --replace-upstream routes the
	// client's default public source to. Empty means the flag is unsupported.
	upstream  string
	flags     []string
	configure func(s *setup) error
}

var clients = map[string]client{
	"npm":        {format: "npm", flags: npmFlags, configure: configureNpm},
	"bun":        {format: "npm", flags: npmFlags, configure: configureNpm},
	"yarn":       {format: "npm", flags: npmFlags, configure: configureYarn},
	"pnpm":       {format: "npm", flags: npmFlags, configure: configurePnpm},
	"cargo":      {format: "cargo", upstream: "crates-io", configure: configureCargo},
	"bundler":    {format: "rubygems", upstream: "rubygems", configure: configureBundler},
	"gem":        {format: "rubygems", upstream: "rubygems", configure: configureGem},
	"helm":       {format: "helm", configure: configureHelm},
	"mamba":      {format: "conda", upstream: "conda-forge", configure: configureMamba},
	"micromamba": {format: "conda", upstream: "conda-forge", configure: configureMamba},
	"pixi":       {format: "conda", upstream: "conda-forge", configure: configurePixi},
	"apt":        {format: "debian", flags: []string{"suite", "components", "signing-key"}, configure: configureApt},
	"dnf":        {format: "rpm", flags: rpmFlags, configure: configureYumRepo},
	"yum":        {format: "rpm", flags: rpmFlags, configure: configureYumRepo},
	"zypper":     {format: "rpm", flags: rpmFlags, configure: configureZypper},
	"apk":        {format: "alpine", flags: []string{"branch", "components"}, configure: configureApk},
}

var (
	npmFlags = []string{"scope", "auth-only"}
	rpmFlags = []string{"path", "package-key"}
)

var clientSpecificFlags = []string{"replace-upstream", "scope", "auth-only", "suite", "components", "branch", "path", "signing-key", "package-key"}

// helperClients retrieve the current token through a credential helper unless
// --auth store is set. All other clients save a copy of the token.
var helperClients = []string{"pnpm", "cargo"}

type setup struct {
	ctx                     context.Context
	clientName              string
	repo                    repository
	token                   string
	opts                    options
	store                   bool
	replaceUpstream         bool
	replaceUpstreamExplicit bool
	changes                 []string
	notes                   []string
	elevationNoticeShown    bool
}

func (s *setup) changedFile(path string) {
	if !slices.Contains(s.changes, path) {
		s.changes = append(s.changes, path)
	}
}

func (s *setup) notef(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

func NewCmdConfigure() *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "configure <client> --org <org-id> --repository <name>",
		Short: "Configure a package manager to use a Depot Packages repository",
		Long: `Configure a package manager to install and publish packages through a Depot Packages repository.

Supported clients: ` + strings.Join(clientNames(), ", ") + `.

pnpm and Cargo retrieve the current Depot token through a credential helper. Other clients save a copy of the token in their own configuration, so rerun this command when the token changes. Use --auth store to save a token for pnpm and Cargo too.

The token comes from DEPOT_TOKEN when it is set, otherwise from depot login.`,
		Example: `  depot packages configure npm --org <org-id> --repository npmjs
  depot packages configure cargo --org <org-id> --repository crates-io
  depot packages configure apt --org <org-id> --repository debian --suite bookworm --components main`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: clientNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigure(cmd, args[0], opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.org, "org", "", "Depot organization ID (defaults to the current organization)")
	flags.StringVar(&opts.repository, "repository", "", "Repository name")
	flags.StringVar(&opts.auth, "auth", authDefault, `Authentication method: "store" saves the token in the client's configuration instead of using a credential helper`)
	flags.BoolVar(&opts.replaceUpstream, "replace-upstream", false, "Route the client's default public registry through this repository (defaults to true for the built-in cache of that registry)")
	flags.StringVar(&opts.scope, "scope", "", "npm clients: route only this package scope, such as @acme, to the repository")
	flags.BoolVar(&opts.authOnly, "auth-only", false, "npm clients: save credentials for the repository without changing the registry")
	flags.StringVar(&opts.suite, "suite", "", "apt: distribution suite, such as bookworm (defaults to this system's codename)")
	flags.StringSliceVar(&opts.components, "components", nil, "apt and apk: repository components (defaults to main)")
	flags.StringVar(&opts.branch, "branch", "", "apk: Alpine branch, such as v3.22 (defaults to this system's branch)")
	flags.StringVar(&opts.path, "path", "", "dnf, yum, and zypper: distribution path in the repository, such as el/9 or 9/BaseOS/$basearch/os")
	flags.StringVar(&opts.signingKey, "signing-key", "", `apt: "repository" installs the repository's signing key, "upstream" uses the distribution keyring (defaults to "upstream" for built-in caches)`)
	flags.StringVar(&opts.packageKey, "package-key", "", `dnf, yum, and zypper: local key file that verifies package signatures, or "none" to skip package signature checks`)
	_ = cmd.MarkFlagRequired("repository")

	return cmd
}

func clientNames() []string {
	names := make([]string, 0, len(clients))
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runConfigure(cmd *cobra.Command, clientName string, opts options) error {
	c, ok := clients[clientName]
	if !ok {
		return fmt.Errorf("unsupported client %q; supported clients: %s", clientName, strings.Join(clientNames(), ", "))
	}

	flags := cmd.Flags()
	for _, name := range clientSpecificFlags {
		allowed := slices.Contains(c.flags, name) || (name == "replace-upstream" && c.upstream != "")
		if flags.Changed(name) && !allowed {
			return fmt.Errorf("--%s isn't supported for %s", name, clientName)
		}
	}

	if opts.auth != authDefault && opts.auth != authStore {
		return fmt.Errorf(`invalid --auth %q: the only supported value is "store"`, opts.auth)
	}

	if opts.org == "" {
		opts.org = config.GetCurrentOrganization()
	}
	if opts.org == "" {
		return fmt.Errorf("specify the organization with --org <org-id>")
	}

	repo, err := newRepository(opts.org, c.format, opts.repository)
	if err != nil {
		return err
	}

	token, err := resolveToken()
	if err != nil {
		return err
	}

	replaceUpstream := opts.replaceUpstream
	if !flags.Changed("replace-upstream") {
		replaceUpstream = c.upstream != "" && repo.name == c.upstream
	}

	s := &setup{
		ctx:                     cmd.Context(),
		clientName:              clientName,
		repo:                    repo,
		token:                   token,
		opts:                    opts,
		store:                   opts.auth == authStore || !slices.Contains(helperClients, clientName),
		replaceUpstream:         replaceUpstream,
		replaceUpstreamExplicit: flags.Changed("replace-upstream"),
	}

	if err := c.configure(s); err != nil {
		return err
	}

	s.printReport(cmd.OutOrStdout())
	return nil
}

func (s *setup) printReport(w io.Writer) {
	fmt.Fprintf(w, "Configured %s for %s\n", s.clientName, s.repo.url())
	for _, path := range s.changes {
		fmt.Fprintf(w, "  updated %s\n", path)
	}
	if s.store {
		fmt.Fprintln(w, "The token is saved in the client's configuration. Rerun this command when the token changes.")
	}
	for _, note := range s.notes {
		fmt.Fprintf(w, "\n%s\n", note)
	}
}
