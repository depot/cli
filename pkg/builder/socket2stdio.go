package builder

import (
	"io"
	"net"
	"os"
	"time"
)

func socket2stdio(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return err
	}

	stdin2conn := make(chan error, 1)
	conn2stdout := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		stdin2conn <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		conn2stdout <- err
	}()

	select {
	case err := <-stdin2conn:
		if err != nil {
			return err
		}
		err = <-conn2stdout
		if err != nil {
			return err
		}
	case err = <-conn2stdout:
		if err != nil {
			return err
		}
	}
	return err
}
