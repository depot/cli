package dagger

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/depot/cli/pkg/machine"
)

// LocalListener returns a listener that listens on a random port on localhost.
func LocalListener() (net.Listener, string, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, "", err
	}
	addr := "tcp://" + l.Addr().(*net.TCPAddr).String()
	return l, addr, nil
}

type Proxy struct {
	listener net.Listener
	builder  *machine.Machine
	done     chan struct{}

	mu  sync.Mutex
	err error
}

func NewProxy(listener net.Listener, builder *machine.Machine) *Proxy {
	return &Proxy{
		listener: listener,
		builder:  builder,
		done:     make(chan struct{}),
	}
}

func (p *Proxy) Start(ctx context.Context) error {
	defer func() { _ = p.listener.Close() }()

	wg := &sync.WaitGroup{}
	go p.run(p.listener, wg)
	<-ctx.Done()

	_ = p.listener.Close()
	p.Stop()
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Proxy) Stop() {
	if p.done == nil {
		return
	}
	close(p.done)
	p.done = nil
}

func (p *Proxy) run(listener net.Listener, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-p.done:
			return
		default:
			connection, err := listener.Accept()
			if err == nil {
				wg.Add(1)
				go p.handle(connection)
			} else {
				p.mu.Lock()
				p.err = err
				p.mu.Unlock()
			}
		}
	}
}

func (p *Proxy) handle(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	// TODO: context canceling?
	remote, err := machine.TLSConn(context.Background(), p.builder)
	if err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		return
	}
	defer func() { _ = remote.Close() }()

	wg := &sync.WaitGroup{}
	wg.Add(2)
	go p.copy(remote, connection, wg)
	go p.copy(connection, remote, wg)
	wg.Wait()
}

func (p *Proxy) copy(from, to net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()
	select {
	case <-p.done:
		return
	default:
		if _, err := io.Copy(to, from); err != nil {
			p.mu.Lock()
			p.err = err
			p.mu.Unlock()
			p.Stop()
			return
		}
	}
}
