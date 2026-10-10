package main

import (
	"bytes"
	"io"
)

var (
	http2ServerLog = []byte("http2: server")
	sessionPeer    = []byte("localhost")
)

// logFilter is the output of the standard logger. It drops the lines that
// the HTTP/2 server of a buildkit session logs when the session closes
// during its handshake, and writes every other line. buildx opens such
// short sessions to read the capabilities of a builder, and the build is
// not affected. The peer of a session connection is always "localhost".
type logFilter struct {
	out io.Writer
}

func (f logFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, http2ServerLog) && bytes.Contains(p, sessionPeer) {
		return len(p), nil
	}
	return f.out.Write(p)
}
