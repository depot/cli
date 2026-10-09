package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

type imageSummary struct {
	Present    bool              `json:"present"`
	Os         string            `json:"os,omitempty"`
	Arch       string            `json:"arch,omitempty"`
	Variant    string            `json:"variant,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Env        []string          `json:"env,omitempty"`
	Cmd        []string          `json:"cmd,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
	Layers     int               `json:"layers,omitempty"`
}

type registryResult struct {
	Present     bool                    `json:"present"`
	MediaType   string                  `json:"mediaType,omitempty"`
	Annotations map[string]string       `json:"annotations,omitempty"`
	Manifests   []registryManifestEntry `json:"manifests,omitempty"`
	Layers      int                     `json:"layers,omitempty"`
	Config      *imageConfigSummary     `json:"config,omitempty"`
}

type registryManifestEntry struct {
	Platform    string            `json:"platform"`
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Layers      int               `json:"layers,omitempty"`
	Config      *imageConfigSummary
}

type imageConfigSummary struct {
	Architecture string            `json:"architecture"`
	Os           string            `json:"os"`
	Labels       map[string]string `json:"labels,omitempty"`
	Env          []string          `json:"env,omitempty"`
	Cmd          []string          `json:"cmd,omitempty"`
}

func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func canonicalJSON(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	sortMetadataTargets(v)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}

// inspectImage summarizes a local image. Scenarios that build the same
// content share one image, so the summary and the returned tags include only
// the tags of this run.
func inspectImage(ctx context.Context, ref, runID string, norm func(string) string) (imageSummary, []string) {
	out, err := dockerCommand(ctx, "image", "inspect", ref)
	if err != nil {
		return imageSummary{}, nil
	}
	var inspected []struct {
		Os           string
		Architecture string
		Variant      string
		RepoTags     []string
		Config       struct {
			Labels     map[string]string
			Env        []string
			Cmd        []string
			Entrypoint []string
			WorkingDir string
		}
		RootFS struct {
			Layers []string
		}
	}
	if err := json.Unmarshal([]byte(out), &inspected); err != nil || len(inspected) == 0 {
		return imageSummary{}, nil
	}
	img := inspected[0]
	var tags, runTags []string
	for _, t := range img.RepoTags {
		if strings.Contains(t, runID) {
			runTags = append(runTags, t)
			tags = append(tags, norm(t))
		}
	}
	sort.Strings(tags)
	return imageSummary{
		Present:    true,
		Os:         img.Os,
		Arch:       img.Architecture,
		Variant:    img.Variant,
		Tags:       tags,
		Labels:     img.Config.Labels,
		Env:        img.Config.Env,
		Cmd:        img.Config.Cmd,
		Entrypoint: img.Config.Entrypoint,
		WorkingDir: img.Config.WorkingDir,
		Layers:     len(img.RootFS.Layers),
	}, runTags
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
	Platform    *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

type ociDocument struct {
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations"`
	Manifests   []ociDescriptor   `json:"manifests"`
	Layers      []ociDescriptor   `json:"layers"`
	Config      ociDescriptor     `json:"config"`
}

func registryGet(ctx context.Context, env *environment, url, accept string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.SetBasicAuth(authUser, authPassword)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: env.certificates.pool()}}}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", res.StatusCode)
	}
	return data, res.Header.Get("Content-Type"), nil
}

func splitReference(env *environment, ref string) (repo, tag string) {
	ref = strings.TrimPrefix(ref, env.registryPushHost()+"/")
	ref = strings.TrimPrefix(ref, env.authRegistryHost()+"/")
	if i := strings.LastIndex(ref, ":"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func inspectRegistry(ctx context.Context, env *environment, ref string, norm func(string) string) registryResult {
	repo, tag := splitReference(env, ref)
	host := env.registryPullHost()
	if strings.HasPrefix(ref, env.authRegistryHost()+"/") {
		host = net.JoinHostPort("localhost", authRegistryPort)
	}
	base := fmt.Sprintf("https://%s/v2/%s", host, repo)
	data, mediaType, err := registryGet(ctx, env, base+"/manifests/"+tag, manifestAccept)
	if err != nil {
		return registryResult{}
	}
	var doc ociDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return registryResult{Present: true, MediaType: mediaType}
	}
	if doc.MediaType == "" {
		doc.MediaType = mediaType
	}
	result := registryResult{Present: true, MediaType: doc.MediaType, Annotations: normalizeMap(doc.Annotations, norm)}
	if isCacheIndex(doc) {
		// The layers in a registry cache depend on what other builds left in
		// the buildkitd cache, so only the shape of the cache is compared.
		result.Manifests = []registryManifestEntry{{MediaType: "application/vnd.buildkit.cacheconfig.v0"}}
		result.Layers = min(len(doc.Manifests)-1, 1)
		return result
	}
	if len(doc.Manifests) == 0 {
		result.Layers = len(doc.Layers)
		result.Config = fetchConfig(ctx, env, base, doc.Config.Digest)
		return result
	}
	for _, m := range doc.Manifests {
		entry := registryManifestEntry{MediaType: m.MediaType, Annotations: normalizeMap(m.Annotations, norm)}
		if m.Platform != nil {
			entry.Platform = strings.TrimSuffix(m.Platform.OS+"/"+m.Platform.Architecture+"/"+m.Platform.Variant, "/")
		}
		if child, _, err := registryGet(ctx, env, base+"/manifests/"+m.Digest, manifestAccept); err == nil {
			var childDoc ociDocument
			if json.Unmarshal(child, &childDoc) == nil {
				entry.Layers = len(childDoc.Layers)
				if !strings.Contains(m.MediaType, "index") {
					entry.Config = fetchConfig(ctx, env, base, childDoc.Config.Digest)
				}
			}
		}
		result.Manifests = append(result.Manifests, entry)
	}
	sort.Slice(result.Manifests, func(i, j int) bool {
		a, b := result.Manifests[i], result.Manifests[j]
		ka, _ := json.Marshal(a)
		kb, _ := json.Marshal(b)
		return bytes.Compare(ka, kb) < 0
	})
	return result
}

func fetchConfig(ctx context.Context, env *environment, base, digest string) *imageConfigSummary {
	if digest == "" {
		return nil
	}
	data, _, err := registryGet(ctx, env, base+"/blobs/"+digest, "")
	if err != nil {
		return nil
	}
	var cfg struct {
		Architecture string `json:"architecture"`
		Os           string `json:"os"`
		Config       struct {
			Labels map[string]string `json:"Labels"`
			Env    []string          `json:"Env"`
			Cmd    []string          `json:"Cmd"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &imageConfigSummary{Architecture: cfg.Architecture, Os: cfg.Os, Labels: cfg.Config.Labels, Env: cfg.Config.Env, Cmd: cfg.Config.Cmd}
}

func normalizeMap(m map[string]string, norm func(string) string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = norm(v)
	}
	return out
}

// sortMetadataTargets sorts the target list in a metadata file. Earlier
// versions of the CLI wrote the list in random order.
func sortMetadataTargets(v any) {
	root, ok := v.(map[string]any)
	if !ok {
		return
	}
	build, ok := root["depot.build"].(map[string]any)
	if !ok {
		return
	}
	targets, ok := build["targets"].([]any)
	if !ok {
		return
	}
	sort.Slice(targets, func(i, j int) bool { return fmt.Sprint(targets[i]) < fmt.Sprint(targets[j]) })
}

func isCacheIndex(doc ociDocument) bool {
	for _, m := range doc.Manifests {
		if strings.Contains(m.MediaType, "buildkit.cacheconfig") {
			return true
		}
	}
	return false
}
