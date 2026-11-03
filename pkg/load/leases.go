package load

import (
	"context"
	"time"

	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	"github.com/depot/cli/pkg/buildxdriver"
)

// exportLeaseLabel is the exporter response key that holds the lease that
// keeps an exported image from garbage collection.
const exportLeaseLabel = "depot/session.id"

// leaseDeletionTimeout limits the time that DeleteExportLeases waits for the
// builders.
const leaseDeletionTimeout = 30 * time.Second

// DeleteExportLeases removes the long-lived leases we use to inhibit garbage collection of exported images.
// It runs also when ctx is canceled, such as after another load fails,
// because a remaining lease keeps the image from garbage collection.
func DeleteExportLeases(ctx context.Context, responses []buildxdriver.TargetResponse) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseDeletionTimeout)
	defer cancel()
	for _, res := range responses {
		for _, nodeRes := range res.NodeResponses {
			if nodeRes.SolveResponse == nil || nodeRes.Driver == nil {
				continue
			}
			leaseID := nodeRes.SolveResponse.ExporterResponse[exportLeaseLabel]
			if leaseID == "" {
				continue
			}

			conn, err := nodeRes.Driver.Conn(ctx)
			if err != nil {
				continue
			}
			_, _ = leasesapi.NewLeasesClient(conn).Delete(ctx, &leasesapi.DeleteRequest{ID: leaseID})
		}
	}
}
