package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

type scenario struct {
	Name        string
	Description string
	Fixture     string
	Args        []string
	Env         map[string]string
	Stdin       string
	API         apiBehavior
	Timeout     time.Duration
	Interrupt   time.Duration
	Images      []string
	Registry    []string
	Files       []string
	Expect      expectation
	Accept      map[string]string
	// Exclusive runs the scenario alone, after the other scenarios, because
	// its result depends on the load of the machine.
	Exclusive bool
	// Program is the program to run: "depot" (the default) or "docker".
	Program string
	// Prepare runs before the program and Cleanup runs after it.
	Prepare func(ctx context.Context, r *runInfo) error
	Cleanup func(ctx context.Context, r *runInfo)
	// BaselineDefect describes a defect of the baseline CLI that the
	// candidate fixes. The harness then reports differences as accepted and
	// does not check the expectations against the baseline.
	BaselineDefect string
}

type expectation struct {
	ExitCode       int
	StdoutContains []string
	StderrContains []string
	StderrExcludes []string
	FinishBuild    []string
	Check          func(o *observation) error
}

type observation struct {
	ExitCode int                       `json:"exitCode"`
	Stdout   string                    `json:"stdout"`
	Stderr   string                    `json:"stderr"`
	Files    map[string]string         `json:"files,omitempty"`
	Images   map[string]imageSummary   `json:"images,omitempty"`
	Registry map[string]registryResult `json:"registry,omitempty"`
	API      apiSummary                `json:"api"`
	APIJSON  string                    `json:"-"`
	Duration time.Duration             `json:"duration"`
}

// runInfo describes one run of a scenario for its Prepare and Cleanup steps.
type runInfo struct {
	ID         string
	WorkDir    string
	CertDir    string
	Binary     *binary
	Env        []string
	ContextDir string
	HomeDir    string
	Project    string
	Token      string
	API        *fakeAPI
}

func (r *runInfo) dockerConfigDir() string {
	return filepath.Join(r.HomeDir, ".docker")
}

// command runs "depot" or "docker" in the runner container with the
// environment of the run.
func (r *runInfo) command(ctx context.Context, program string, args ...string) (string, error) {
	cmd := runnerCommand(ctx, r, r.ID+"-prepare", program, args)
	out, err := cmd.CombinedOutput()
	_, _ = dockerCommand(context.Background(), "rm", "-f", r.ID+"-prepare")
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", program, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// runnerCommand returns a command that runs a program in a runner
// container. The container joins the network namespace of the builders and
// mounts the run directory at the same path as on the host.
func runnerCommand(ctx context.Context, r *runInfo, name, program string, args []string) *exec.Cmd {
	dockerArgs := []string{
		"run", "--rm", "-i", "--name", name,
		"--network", "container:" + namespaceOwner,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", r.WorkDir + ":" + r.WorkDir,
		"-v", r.Binary.LinuxPath + ":/usr/local/bin/depot:ro",
		"-v", r.CertDir + ":" + r.CertDir + ":ro",
		"-w", r.ContextDir,
	}
	dockerArgs = append(dockerArgs, userArgs()...)
	for _, e := range r.Env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	if program == "depot" {
		program = "/usr/local/bin/depot"
	}
	dockerArgs = append(dockerArgs, runnerImage, program)
	return exec.CommandContext(ctx, "docker", append(dockerArgs, args...)...)
}

type runContext struct {
	env       *environment
	api       *fakeAPI
	workRoot  string
	fixtures  string
	keepFiles bool
}

func (rc *runContext) execute(ctx context.Context, s scenario, bin *binary) (*observation, error) {
	runID := strings.ToLower(fmt.Sprintf("%s-%s-%d", sanitize(s.Name), bin.Label, time.Now().UnixNano()%1_000_000))
	workDir := filepath.Join(rc.workRoot, "runs", runID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	if !rc.keepFiles {
		defer os.RemoveAll(workDir)
	}

	homeDir := filepath.Join(workDir, ".home")
	contextDir := filepath.Join(workDir, "context")
	if err := os.MkdirAll(filepath.Join(homeDir, ".docker"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".docker", "config.json"), rc.env.dockerConfig(), 0o600); err != nil {
		return nil, err
	}
	if s.Fixture != "" {
		if err := copyTree(filepath.Join(rc.fixtures, s.Fixture), contextDir); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return nil, err
	}

	project := "vtproject"
	if s.Prepare != nil {
		project = "vt" + strings.ReplaceAll(runID, "-", "")
	}
	token := "user-token-" + runID
	session := rc.api.openSession(runID, token, project, s.API)

	vars := map[string]string{
		"{{run}}":            runID,
		"{{registry}}":       rc.env.registryPushHost(),
		"{{registry-local}}": rc.env.registryPullHost(),
		"{{registry-auth}}":  rc.env.authRegistryHost(),
		"{{workdir}}":        contextDir,
		"{{project}}":        project,
		"{{git}}":            rc.env.gitURL(),
	}
	expand := func(v string) string {
		for k, r := range vars {
			v = strings.ReplaceAll(v, k, r)
		}
		return v
	}

	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = expand(a)
	}

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env := rc.commandEnv(homeDir, token, project, s, expand)
	if rc.keepFiles {
		_ = os.WriteFile(filepath.Join(workDir, "environment"), []byte(strings.Join(env, "\n")+"\n"), 0o600)
	}
	info := &runInfo{ID: runID, WorkDir: workDir, CertDir: rc.env.certDir, Binary: bin, Env: env, ContextDir: contextDir, HomeDir: homeDir, Project: project, Token: token, API: rc.api}
	if s.Cleanup != nil {
		defer s.Cleanup(context.Background(), info)
	}
	if s.Prepare != nil {
		if err := s.Prepare(ctx, info); err != nil {
			return nil, fmt.Errorf("prepare: %w", err)
		}
	}

	program := "depot"
	if s.Program == "docker" {
		program = "docker"
	}
	cmd := runnerCommand(runCtx, info, runID, program, args)
	defer func() { _, _ = dockerCommand(context.Background(), "rm", "-f", runID) }()
	if s.Stdin != "" {
		cmd.Stdin = strings.NewReader(expand(s.Stdin))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if s.Interrupt > 0 {
		go func() {
			select {
			case <-time.After(s.Interrupt):
				_ = cmd.Process.Signal(syscall.SIGINT)
			case <-runCtx.Done():
			}
		}()
	}
	err := cmd.Wait()
	obs := &observation{Duration: time.Since(start), ExitCode: 0}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			obs.ExitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}
	if runCtx.Err() != nil && ctx.Err() == nil {
		obs.ExitCode = -1
	}

	time.Sleep(200 * time.Millisecond)
	obs.API = session.summary()

	norm := rc.normalizer(runID, contextDir, homeDir, project)
	obs.APIJSON = norm(toJSON(obs.API))
	obs.Stdout = norm(stdout.String())
	obs.Stderr = norm(stderr.String())

	obs.Files = map[string]string{}
	for _, f := range s.Files {
		obs.Files[f] = norm(captureFile(filepath.Join(contextDir, expand(f))))
	}

	obs.Images = map[string]imageSummary{}
	for _, ref := range s.Images {
		ref = expand(ref)
		summary, tags := inspectImage(ctx, ref, runID, norm)
		obs.Images[norm(ref)] = summary
		for _, tag := range tags {
			_, _ = dockerCommand(ctx, "rmi", tag)
		}
	}

	obs.Registry = map[string]registryResult{}
	for _, ref := range s.Registry {
		ref = expand(ref)
		obs.Registry[norm(ref)] = inspectRegistry(ctx, rc.env, ref, norm)
	}
	return obs, nil
}

func (rc *runContext) commandEnv(homeDir, token, project string, s scenario, expand func(string) string) []string {
	env := []string{
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + filepath.Join(homeDir, ".config"),
		"DOCKER_CONFIG=" + filepath.Join(homeDir, ".docker"),
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"DEPOT_API_URL=" + rc.api.containerURL(),
		"SSL_CERT_FILE=" + filepath.Join(rc.env.certDir, "ca.pem"),
		"DEPOT_TOKEN=" + token,
		"DEPOT_PROJECT_ID=" + project,
		"DEPOT_NO_UPDATE_NOTIFIER=1",
		"DEPOT_ERROR_TELEMETRY=0",
		"DEPOT_DISABLE_OTEL=1",
		"TERM=dumb",
		"NO_COLOR=1",
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := expand(s.Env[k])
		filtered := env[:0]
		for _, e := range env {
			if !strings.HasPrefix(e, k+"=") {
				filtered = append(filtered, e)
			}
		}
		env = filtered
		if v != "<unset>" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

var (
	ansiPattern     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	timePattern     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	durationPattern = regexp.MustCompile(`\b\d+(\.\d+)?(ms|s|m)\b`)
	digestPattern   = regexp.MustCompile(`sha256:[0-9a-f]{64}`)
	hexIDPattern    = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	sizePattern     = regexp.MustCompile(`\b\d+(\.\d+)?\s?(B|kB|KB|MB|MiB|GB|KiB)\b`)
	stepPattern     = regexp.MustCompile(`(?m)^#\d+ `)
	tempPattern     = regexp.MustCompile(`/(private/)?(var/folders|tmp)/[^\s"':]*`)
	leasePattern    = regexp.MustCompile(`\b[a-z0-9]{25}\b`)
)

func (rc *runContext) normalizer(runID, contextDir, homeDir, project string) func(string) string {
	replacements := rc.env.replacements()
	return func(s string) string {
		s = ansiPattern.ReplaceAllString(s, "")
		s = strings.ReplaceAll(s, "\r", "")
		for from, to := range replacements {
			s = strings.ReplaceAll(s, from, to)
		}
		s = strings.ReplaceAll(s, contextDir, "<workdir>")
		s = strings.ReplaceAll(s, homeDir, "<home>")
		s = strings.ReplaceAll(s, project, "<project>")
		s = strings.ReplaceAll(s, runID, "<run>")
		s = timePattern.ReplaceAllString(s, "<time>")
		s = digestPattern.ReplaceAllString(s, "<digest>")
		s = hexIDPattern.ReplaceAllString(s, "<hex>")
		s = durationPattern.ReplaceAllString(s, "<duration>")
		s = sizePattern.ReplaceAllString(s, "<size>")
		s = stepPattern.ReplaceAllString(s, "#N ")
		s = tempPattern.ReplaceAllString(s, "<tmp>")
		s = leasePattern.ReplaceAllString(s, "<id>")
		return s
	}
}

func sanitize(s string) string {
	return strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(s, "-"), "-")
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func captureFile(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "<missing>"
	}
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return "<unreadable>"
		}
		if listing, ok := tarListing(data); ok {
			return listing
		}
		return canonicalJSON(data)
	}
	var lines []string
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == path {
			return nil
		}
		rel, _ := filepath.Rel(path, p)
		if d.IsDir() {
			lines = append(lines, rel+"/")
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			lines = append(lines, rel+" -> "+target)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			lines = append(lines, rel+" <unreadable>")
			return nil
		}
		lines = append(lines, fmt.Sprintf("%s %s", rel, shortHash(data)))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// tarListing lists the entries of a tar archive. Archives that contain
// image layouts include timestamps, so only names and types are compared.
func tarListing(data []byte) (string, bool) {
	if len(data) < 512 || !bytes.Equal(data[257:262], []byte("ustar")) {
		return "", false
	}
	tr := tar.NewReader(bytes.NewReader(data))
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		name := hdr.Name
		if strings.HasPrefix(name, "blobs/sha256/") {
			name = "blobs/sha256/<blob>"
		}
		names = append(names, fmt.Sprintf("%s %c", name, hdr.Typeflag))
	}
	sort.Strings(names)
	return strings.Join(slices.Compact(names), "\n"), true
}

// userArgs runs the runner as the user of the harness on Linux, so that the
// harness can remove the files that a run writes. Docker on macOS maps file
// ownership to the user already.
func userArgs() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	args := []string{"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	if info, err := os.Stat("/var/run/docker.sock"); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			args = append(args, "--group-add", fmt.Sprint(st.Gid))
		}
	}
	return args
}
