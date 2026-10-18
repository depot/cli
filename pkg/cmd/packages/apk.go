package packages

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	apkRepositoriesFile = "/etc/apk/repositories"
	rootNetrcFile       = "/root/.netrc"
)

var (
	apkKeyNamePattern          = regexp.MustCompile(`^[A-Za-z0-9._@-]+\.pub$`)
	apkBranchPattern           = regexp.MustCompile(`^(v[0-9]+\.[0-9]+|edge)$`)
	apkRepositoryBranchPattern = regexp.MustCompile(`/(v[0-9]+\.[0-9]+|edge)/[A-Za-z0-9_-]+/?$`)
	apkVersionPattern          = regexp.MustCompile(`apk-tools ([0-9]+)\.([0-9]+)\.([0-9]+)`)
)

func configureApk(s *setup) error {
	if err := s.requireSystemClient(); err != nil {
		return err
	}
	if err := requireApkNetrcSupport(s); err != nil {
		return err
	}

	branch := s.opts.branch
	if branch == "" {
		branch = detectAlpineBranch()
	}
	if branch == "" {
		return fmt.Errorf("could not detect the Alpine branch: specify --branch, such as --branch v3.22")
	}
	if !apkBranchPattern.MatchString(branch) {
		return fmt.Errorf("invalid --branch %q: use a branch such as v3.22 or edge", branch)
	}

	components := s.opts.components
	if len(components) == 0 {
		components = []string{"main"}
	}

	keyName, err := s.download(s.repo.url() + "keys/current")
	if err != nil {
		return err
	}
	name := strings.TrimSpace(string(keyName))
	if !apkKeyNamePattern.MatchString(name) {
		return fmt.Errorf("the repository returned an invalid key name %q", name)
	}
	key, err := s.download(s.repo.url() + "keys/" + name)
	if err != nil {
		return err
	}
	if err := s.writeSystemFile("/etc/apk/keys/"+name, key, 0644); err != nil {
		return err
	}

	netrc, err := s.readSystemFile(rootNetrcFile)
	if err != nil {
		return err
	}
	updated, err := setNetrcMachine(netrc, s.repo.hostname(), "depot", s.token)
	if err != nil {
		return fmt.Errorf("could not update %s: %w", rootNetrcFile, err)
	}
	if err := s.writeSystemFile(rootNetrcFile, updated, 0600); err != nil {
		return err
	}

	repositories, err := os.ReadFile(apkRepositoriesFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(repositories), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(strings.TrimSpace(line), s.repo.url()) {
			lines = append(lines, line)
		}
	}
	for _, component := range components {
		lines = append(lines, s.repo.url()+branch+"/"+component)
	}
	if err := s.writeSystemFile(apkRepositoriesFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return err
	}

	s.notef("Existing apk repositories are unchanged; remove direct upstream entries from %s if all downloads should use Depot.", apkRepositoriesFile)
	return nil
}

func detectAlpineBranch() string {
	repositories, _ := os.ReadFile(apkRepositoriesFile)
	release, _ := os.ReadFile("/etc/alpine-release")
	return alpineBranch(string(repositories), string(release))
}

// alpineBranch prefers the branch of the existing untagged repositories,
// because the release file of an edge system holds a development version.
func alpineBranch(repositories, release string) string {
	for _, line := range strings.Split(repositories, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "@") {
			continue
		}
		if match := apkRepositoryBranchPattern.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}

	version := strings.TrimSpace(release)
	if strings.Contains(version, "_") {
		return "edge"
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return ""
	}
	return "v" + parts[0] + "." + parts[1]
}

// requireApkNetrcSupport checks for apk-tools 2.14.12 or later, the first
// version that reads credentials from .netrc.
func requireApkNetrcSupport(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "apk", "--version").Output()
	if err != nil {
		return fmt.Errorf("could not determine the apk-tools version: %w", err)
	}
	match := apkVersionPattern.FindStringSubmatch(string(out))
	if match == nil {
		return fmt.Errorf("could not determine the apk-tools version from %q", strings.TrimSpace(string(out)))
	}

	version := [3]int{}
	for i := range version {
		version[i], _ = strconv.Atoi(match[i+1])
	}
	minimum := [3]int{2, 14, 12}
	for i := range version {
		if version[i] != minimum[i] {
			if version[i] < minimum[i] {
				return fmt.Errorf("apk-tools %s.%s.%s doesn't read .netrc credentials: upgrade to 2.14.12 or later, or use an authenticated repository URL", match[1], match[2], match[3])
			}
			break
		}
	}
	return nil
}

type netrcToken struct {
	text    string
	start   int
	keyword bool
}

// netrcTokens splits a .netrc file into tokens with their byte offsets. A
// token that starts with # begins a comment that runs to the end of the line.
func netrcTokens(data string) []netrcToken {
	var tokens []netrcToken
	for i := 0; i < len(data); {
		switch {
		case data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n':
			i++
		case data[i] == '#':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case data[i] == '"':
			start := i
			i++
			for i < len(data) && data[i] != '"' {
				if data[i] == '\\' {
					i++
				}
				i++
			}
			i++
			tokens = append(tokens, netrcToken{text: data[start:min(i, len(data))], start: start})
		default:
			start := i
			for i < len(data) && !strings.ContainsRune(" \t\r\n", rune(data[i])) {
				i++
			}
			tokens = append(tokens, netrcToken{text: data[start:i], start: start})
		}
	}

	for i := 0; i < len(tokens); i++ {
		tokens[i].keyword = true
		switch tokens[i].text {
		case "machine", "login", "password", "account":
			i++
		}
	}
	return tokens
}

func (t netrcToken) is(keyword string) bool {
	return t.keyword && t.text == keyword
}

// value decodes a quoted token, in which a backslash escapes the next
// character.
func (t netrcToken) value() string {
	if len(t.text) < 2 || t.text[0] != '"' {
		return t.text
	}
	var value strings.Builder
	inner := strings.TrimSuffix(t.text[1:], `"`)
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
		}
		value.WriteByte(inner[i])
	}
	return value.String()
}

// setNetrcMachine replaces the entry for machine and keeps the rest of the
// file, including comments and formatting, unchanged.
func setNetrcMachine(data []byte, machine, login, password string) ([]byte, error) {
	if strings.ContainsAny(password, " \t\n\"#") {
		return nil, fmt.Errorf("the token contains characters .netrc can't store")
	}

	text := string(data)
	tokens := netrcTokens(text)
	for _, token := range tokens {
		if token.is("macdef") {
			return nil, fmt.Errorf("macdef entries aren't supported")
		}
	}

	var out strings.Builder
	kept := 0
	for i := 0; i < len(tokens); i++ {
		if !tokens[i].is("machine") || i+1 >= len(tokens) || tokens[i+1].value() != machine {
			continue
		}
		last := i + 1
		for j := i + 2; j < len(tokens) && !tokens[j].is("machine") && !tokens[j].is("default"); j++ {
			last = j
		}
		start := tokens[i].start
		end := tokens[last].start + len(tokens[last].text)
		lineEnd := end
		for lineEnd < len(text) && (text[lineEnd] == ' ' || text[lineEnd] == '\t' || text[lineEnd] == '\r') {
			lineEnd++
		}
		if (start == 0 || text[start-1] == '\n') && (lineEnd == len(text) || text[lineEnd] == '\n') {
			end = min(lineEnd+1, len(text))
		}
		out.WriteString(text[kept:start])
		kept = end
	}
	out.WriteString(text[kept:])

	result := out.String()
	entry := fmt.Sprintf("machine %s login %s password %s\n", machine, login, password)

	// Clients stop searching at the default entry, so the new entry must come
	// before it.
	for _, token := range netrcTokens(result) {
		if token.is("default") {
			return []byte(result[:token.start] + entry + result[token.start:]), nil
		}
	}

	if result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return []byte(result + entry), nil
}
