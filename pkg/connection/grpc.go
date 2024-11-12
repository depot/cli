package connection

import (
	"context"
	"net"
	"sync"

	"github.com/depot/cli/pkg/machine"
)

type GRPCProxy struct {
	listener net.Listener
	builder  *machine.Machine
	done     chan struct{}

	mu  sync.Mutex
	err error
}

func NewGRPCProxy(listener net.Listener, builder *machine.Machine) *GRPCProxy {
	return &GRPCProxy{
		listener: listener,
		builder:  builder,
		done:     make(chan struct{}),
	}
}

func (p *GRPCProxy) Start(ctx context.Context) error {
	defer func() { _ = p.listener.Close() }()

	wg := &sync.WaitGroup{}
	go p.run(ctx, p.listener, wg)
	<-ctx.Done()

	_ = p.listener.Close()
	p.Stop()
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *GRPCProxy) Stop() {
	if p.done == nil {
		return
	}
	close(p.done)
	p.done = nil
}

func (p *GRPCProxy) run(ctx context.Context, listener net.Listener, wg *sync.WaitGroup) {
	for {
		select {
		case <-p.done:
			return
		case <-ctx.Done():
			return
		default:
			connection, err := listener.Accept()
			if err == nil {
				defer wg.Done()
				wg.Add(1)
				go p.handle(ctx, connection)
			} else {
				p.mu.Lock()
				p.err = err
				p.mu.Unlock()
			}
		}
	}
}

func (p *GRPCProxy) handle(ctx context.Context, localConn net.Conn) {
	defer func() { _ = localConn.Close() }()
	remote, err := TLSConn(context.Background(), p.builder)
	if err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		return
	}
	defer func() { _ = remote.Close() }()

	buildkitClient, err := BuildkitdClient(ctx, remote, p.builder.Addr)
	if err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		return
	}

	BuildkitProxy(ctx, localConn, buildkitClient, p.builder.Platform)
}
