package load

import (
	"context"
	"fmt"

	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/depot"
)

// DeleteExportLeases removes the long-lived leases we use to inhibit garbage collection of exported images.
func DeleteExportLeases(ctx context.Context, responses []build.DepotBuildResponse) {
	for _, res := range responses {
		for _, nodeRes := range res.NodeResponses {
			if nodeRes.SolveResponse == nil {
				continue
			}
			leaseID := nodeRes.SolveResponse.ExporterResponse[depot.ExportLeaseLabel]
			if leaseID == "" {
				continue
			}

			leasesClient, err := leasesClient(ctx, nodeRes)
			if err != nil {
				// Older versions of buildkitd may not have the leases API exposed.
				continue
			}
			_, _ = leasesClient.Delete(ctx, &leasesapi.DeleteRequest{ID: leaseID})
		}
	}
}

func leasesClient(ctx context.Context, nodeResponse build.DepotNodeResponse) (leasesapi.LeasesClient, error) {
	if nodeResponse.Node.Driver == nil {
		return nil, fmt.Errorf("node %s does not have a driver", nodeResponse.Node.Name)
	}

	client, err := nodeResponse.Node.Driver.Client(ctx)
	if err != nil {
		return nil, err
	}

	if client == nil {
		return nil, fmt.Errorf("node %s does not have a client", nodeResponse.Node.Name)
	}

	return client.LeasesClient(), nil
}
