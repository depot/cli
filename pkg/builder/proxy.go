package builder

import (
	"time"

	"github.com/depot/cli/pkg/dialstdio"
)

func NewProxy(apiHost string, apiKey string, builderID string) error {
	socketServer, err := newSocketProxyServer(apiHost, apiKey, builderID)
	if err != nil {
		return err
	}

	onListening := make(chan error, 1)

	socketServerChan := make(chan error, 1)
	socket2stdioChan := make(chan error, 1)
	go func() {
		socketServerChan <- socketServer.Listen(onListening)
	}()

	err = <-onListening
	if err != nil {
		return err
	}

	go func() {
		socket2stdioChan <- dialstdio.DialStdioTimeout("unix://"+socketServer.socketPath, 10*time.Second)
	}()

	select {
	case err := <-socketServerChan:
		return err
	case err := <-socket2stdioChan:
		return err
	}
}
