package pull

import (
	"fmt"
	"strings"
)

// legacyRegistryHost is the shared registry host that routes to an organization server-side. It is
// only used when the API does not report an organization registry host.
const legacyRegistryHost = "registry.depot.dev"

// registryHost returns the host to pull from. Organization hosts such as
// "5m6p3sp5rj.registry.depot.dev" answer with a Bearer challenge, so Docker reuses one scoped token
// across manifest and blob requests instead of exchanging Basic credentials on every request.
func registryHost(apiRegistryHost string) string {
	if apiRegistryHost == "" {
		return legacyRegistryHost
	}
	return apiRegistryHost
}

// isDepotRegistryHost reports whether host is the shared host or an organization host under it.
func isDepotRegistryHost(host string) bool {
	return host == legacyRegistryHost || strings.HasSuffix(host, "."+legacyRegistryHost)
}

// splitRegistryReference splits "[host/]projectID:tag" into its project ID and tag. Any Depot
// registry host is accepted, including the legacy shared host.
func splitRegistryReference(reference string) (projectID string, tag string, ok bool) {
	remainder := reference
	if host, rest, found := strings.Cut(reference, "/"); found {
		if !isDepotRegistryHost(host) {
			return "", "", false
		}
		remainder = rest
	}

	projectID, tag, found := strings.Cut(remainder, ":")
	if !found || projectID == "" || tag == "" {
		return "", "", false
	}
	return projectID, tag, true
}

// imageReference builds the pull reference for a project image on host.
func imageReference(host, projectID, tag string) string {
	return fmt.Sprintf("%s/%s:%s", host, projectID, tag)
}

// rewriteReferenceHost points an API-provided reference at host, keeping the repository and tag.
// References that are not Depot registry references are returned unchanged.
func rewriteReferenceHost(reference, host string) string {
	currentHost, rest, found := strings.Cut(reference, "/")
	if !found || !isDepotRegistryHost(currentHost) {
		return reference
	}
	return host + "/" + rest
}
