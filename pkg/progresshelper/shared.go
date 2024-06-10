package progresshelper

import (
	"context"
	"os"
	"sync"
	"sync/atomic"

	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
)

var _ progress.Writer = (*SharedPrinter)(nil)

// SharedPrinter is a reference counted progress.Writer that can be used
// to share progress updates between several concurrent builds.
// Originally used for bake files with multiple projects.
//
// the `Wait()` method will wait until all writers have
// run Wait().
type SharedPrinter struct {
	wg      sync.WaitGroup
	printer *progress.Printer
	cancel  context.CancelFunc

	numPrinters atomic.Int32
}

func NewSharedPrinter(mode string) (*SharedPrinter, error) {
	ctx, cancel := context.WithCancel(context.Background())
	printer, err := progress.NewPrinter(ctx, os.Stderr, os.Stderr, mode)
	if err != nil {
		cancel()
		return nil, err
	}

	return &SharedPrinter{
		printer: printer,
		cancel:  cancel,
	}, nil
}

// Add increments the reference count of the writer.
// Each call to Add() should be matched with a call to Wait().
func (w *SharedPrinter) Add() {
	w.wg.Add(1)
	w.numPrinters.Add(1)
}

func (w *SharedPrinter) Wait() error {
	w.wg.Done()
	w.wg.Wait()

	w.cancel()

	lastPrinter := w.numPrinters.Add(-1) == 0

	// The docker progress writer will only return an
	// error if it is a context cancellation error.
	//
	// Only the last printer will be the one to stop the docker printer as
	// the docker printer closes channels.
	if lastPrinter {
		_ = w.printer.Wait()
	}

	return nil
}

func (w *SharedPrinter) Write(status *client.SolveStatus) { w.printer.Write(status) }
func (w *SharedPrinter) ClearLogSource(v interface{})     { w.printer.ClearLogSource(v) }
func (w *SharedPrinter) ValidateLogSource(d digest.Digest, v interface{}) bool {
	return w.printer.ValidateLogSource(d, v)
}
