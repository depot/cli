package sbom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	contentv1 "github.com/containerd/containerd/api/services/content/v1"
	depotbuild "github.com/depot/cli/pkg/buildx/build"
	"github.com/docker/buildx/driver"
	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
)

func Save(ctx context.Context, outputDir string, resp []depotbuild.DepotBuildResponse) error {
	targetPlatforms := map[string]map[string]sbomOutput{}
	for _, buildRes := range resp {
		targetName := buildRes.Name
		for _, nodeRes := range buildRes.NodeResponses {
			sboms, err := decodeNodeResponses(nodeRes)
			if err != nil {
				return err
			}

			if sboms == nil {
				continue
			}

			for _, sbom := range sboms {
				if _, ok := targetPlatforms[targetName]; !ok {
					targetPlatforms[targetName] = map[string]sbomOutput{}
				}
				targetPlatforms[targetName][sbom.Platform] = sbomOutput{driver: nodeRes.Node.Driver, sbom: sbom}
			}
		}
	}

	sboms := withSBOMPaths(targetPlatforms, outputDir)
	if len(sboms) == 0 {
		return nil
	}

	err := os.MkdirAll(outputDir, 0750)
	if err != nil {
		return err
	}

	downloadGroup, ctx := errgroup.WithContext(ctx)
	for _, sbom := range sboms {
		func(sbom sbomOutput) {
			downloadGroup.Go(func() error { return downloadSBOM(ctx, sbom) })
		}(sbom)
	}

	if err := downloadGroup.Wait(); err != nil {
		return err
	}

	return nil
}

type sbomOutput struct {
	driver     driver.Driver
	outputPath string
	sbom       sbomReference
}

// withSBOMPaths determines the output file name based on the number of build targets and platforms.
func withSBOMPaths(targetPlatforms map[string]map[string]sbomOutput, outputDir string) []sbomOutput {
	sboms := []sbomOutput{}

	numBuildTargets := len(targetPlatforms)
	for targetName, platforms := range targetPlatforms {
		numPlatforms := len(platforms)
		for platform, sbom := range platforms {
			platform = strings.ReplaceAll(platform, "/", "_")

			var fileName string
			if numBuildTargets == 1 && numPlatforms == 1 {
				fileName = "sbom.spdx.json"
			} else if numBuildTargets == 1 {
				fileName = fmt.Sprintf("%s.spdx.json", platform)
			} else if numPlatforms == 1 {
				fileName = fmt.Sprintf("%s.spdx.json", targetName)
			} else {
				fileName = fmt.Sprintf("%s_%s.spdx.json", targetName, platform)
			}

			sbom.outputPath = path.Join(outputDir, fileName)
			sboms = append(sboms, sbom)
		}
	}

	return sboms
}

// SBOMsLabel is the key for the SBOM attestation.
const SBOMsLabel = "depot/sboms"

type sbomReference struct {
	// Platform is the specific platform that was scanned.
	Platform string `json:"platform"`
	// Digest is the content digest of the SBOM.
	Digest string `json:"digest"`
	// If an image was created this is the image name and digest of the scanned SBOM.
	Image *ImageSBOM `json:"image"`
}

// ImageSBOM describes an image that is described by an SBOM.
type ImageSBOM struct {
	// Name is the image name and tag.
	Name string `json:"name"`
	// ManifestDigest is the digest of the manifest and can be used
	// to pull the image such as:
	// docker pull goller/xmarks@sha256:6839c1808eab334a9b0f400f119773a0a7d494631c083aef6d3447e3798b544f
	ManifestDigest string `json:"manifest_digest"`
}

// decodeNodeResponses decodes the SBOMs from the node responses. If the
// response does not have SBOMs, nil is returned.
func decodeNodeResponses(nodeRes depotbuild.DepotNodeResponse) ([]sbomReference, error) {
	encodedSBOMs, ok := nodeRes.SolveResponse.ExporterResponse[SBOMsLabel]
	if !ok {
		return nil, nil
	}
	sboms, err := decodeSBOMReferences(encodedSBOMs)
	if err != nil {
		return nil, fmt.Errorf("invalid exported images json: %w", err)
	}

	return sboms, nil
}

func decodeSBOMReferences(encodedSBOMs string) ([]sbomReference, error) {
	b64 := base64.NewDecoder(base64.StdEncoding, bytes.NewBufferString(encodedSBOMs))
	var sboms []sbomReference
	err := json.NewDecoder(b64).Decode(&sboms)
	return sboms, err
}

// downloadSBOM downloads the SBOM and also writes it to the output file.
func downloadSBOM(ctx context.Context, sbom sbomOutput) error {
	client, err := sbom.driver.Client(ctx)
	if err != nil {
		return err
	}

	contentClient := client.ContentClient()
	r, err := contentClient.Read(ctx, &contentv1.ReadContentRequest{Digest: digest.Digest(sbom.sbom.Digest)})
	if err != nil {
		return err
	}

	// Preallocate 1MB for the buffer. This is a guess at the size of the SBOM.
	inner := make([]byte, 0, 1024*1024)
	buf := bytes.NewBuffer(inner)

	for {
		resp, err := r.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		_, err = buf.Write(resp.Data)
		if err != nil {
			return err
		}
	}

	// Strip the in-toto statement header and save the SBOM predicate.
	var statement Statement
	err = json.Unmarshal(buf.Bytes(), &statement)
	if err != nil {
		return err
	}

	octets, err := json.Marshal(statement.Predicate)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(sbom.outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}

	_, err = output.Write(octets)
	if err != nil {
		return err
	}

	return output.Close()
}

// Statement copied from in-toto-golang/in_toto but using json.RawMessage
// to avoid unmarshalling and allocating the subject and predicate.
type Statement struct {
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Subject       json.RawMessage `json:"subject"`
	Predicate     json.RawMessage `json:"predicate"`
}
