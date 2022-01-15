package builder

func NewProxy(apiKey string, builderID string) error {
	socketServer, err := newSocketProxyServer("https://api.depot.dev", apiKey, builderID)
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
		socket2stdioChan <- socket2stdio(socketServer.socketPath)
	}()

	select {
	case err := <-socketServerChan:
		return err
	case err := <-socket2stdioChan:
		return err
	}
}
