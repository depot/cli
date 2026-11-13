package gitremote

import (
	"net/url"
	"strings"
)

// SplitURL extracts the host and repository path without retaining URL credentials.
// It supports HTTPS, SSH, scp-style Git remotes, and Entire remote helpers.
func SplitURL(remoteURL string) (host, path string, ok bool) {
	if !strings.Contains(remoteURL, "://") {
		colon := strings.IndexByte(remoteURL, ':')
		if colon <= 0 || colon == len(remoteURL)-1 {
			return "", "", false
		}
		hostPart := remoteURL[:colon]
		if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		return hostPart, remoteURL[colon+1:], hostPart != ""
	}

	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh" && parsed.Scheme != "entire") {
		return "", "", false
	}
	if parsed.Scheme == "entire" && !(strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".entire.io") || strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".entire.com")) {
		return "", "", false
	}
	return parsed.Hostname(), parsed.Path, true
}
