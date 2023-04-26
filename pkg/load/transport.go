package load

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/opencontainers/go-digest"
)

type Transport struct {
	conn net.Conn

	closed chan struct{}
	write  chan WriteReq
	read   chan *Packet
}

func NewTransport(c net.Conn) *Transport {
	t := &Transport{
		conn:   c,
		closed: make(chan struct{}),
		write:  make(chan WriteReq, 16),
		read:   make(chan *Packet, 128),
	}

	return t
}

func (t *Transport) Run(ctx context.Context, client contentapi.ContentClient) error {
	wg := sync.WaitGroup{}
	defer wg.Wait()

	// Serialize writes to the transport.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case req, ok := <-t.write:
				if !ok {
					return
				}
				n, err := req.packet.Write(t.conn)
				req.recv <- WriteRes{n: n, err: err}
				close(req.recv)
			case <-t.closed:
				return
			}
		}
	}()

	// Deserialize reads from the transport.
	// In a separate goroutine because its read loop is blocking.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(t.read)

		r := NewAttachReader(t.conn)
		for {
			packet, err := ReadPacket(r)
			if err != nil {
				return
			}

			select {
			case t.read <- packet:
			case <-t.closed:
				return
			}
		}
	}()

	// For each read request in the stream, start a goroutine to read
	// from the content store and write blob to the serialized transport channel.
	for {
		select {
		case <-ctx.Done():
			return t.close()
		case packet, ok := <-t.read:
			if !ok {
				return t.close()
			}

			id, dgst, err := packet.BlobRequest()
			if err != nil {
				_, _ = t.Write(ctx, Error(id, 400))
				continue
			}

			wg.Add(1)
			go func(id ID, dgst digest.Digest, client contentapi.ContentClient) {
				defer wg.Done()

				rr := &contentapi.ReadContentRequest{
					Digest: dgst,
				}
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

				rc, err := client.Read(ctx, rr)
				if err != nil {
					_, _ = t.Write(ctx, Error(id, 404))
					return
				}

				for {
					res, err := rc.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						_, _ = t.Write(ctx, Error(id, 404))
						return
					}
					_, err = t.Write(ctx, BlobChunk(id, res.Data))
					if err != nil {
						return
					}
				}

				_, _ = t.Write(ctx, EOF(id))
			}(id, dgst, client)
		}
	}
}

type WriteReq struct {
	packet *Packet
	recv   chan WriteRes
}

type WriteRes struct {
	n   int
	err error
}

func (t *Transport) Write(ctx context.Context, packet *Packet) (int, error) {
	// Single element channel to avoid blocking the writer.
	// We use this "return" channel so we can also watch for closed and timeouts.
	recv := make(chan WriteRes, 1)
	select {
	case t.write <- WriteReq{packet: packet, recv: recv}:
	case <-t.closed:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	select {
	case res, ok := <-recv:
		if !ok {
			return 0, io.EOF
		}
		return res.n, res.err
	case <-t.closed:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (t *Transport) close() error {
	close(t.closed)
	err := t.conn.Close()
	return err
}

// AttachReader is able to remove the docker framing from a docker attach stream.
type AttachReader struct {
	reader *bufio.Reader
	extra  []byte
}

func NewAttachReader(c net.Conn) *AttachReader {
	return &AttachReader{
		reader: bufio.NewReader(c),
	}
}

func (r *AttachReader) Read(bs []byte) (int, error) {
	// Parse docker encapsulated header.
	if len(r.extra) == 0 {
		bs := make([]byte, 8)
		_, err := io.ReadFull(r.reader, bs)
		if err != nil {
			return 0, err
		}
		len := binary.BigEndian.Uint32(bs[4:8])
		isStderr := bs[0] == 2
		if isStderr {
			bs = make([]byte, len)
			_, err := io.ReadFull(r.reader, bs)
			if err != nil {
				return 0, err
			}
			return 0, nil
		} else {
			r.extra = make([]byte, len)
			_, err = io.ReadFull(r.reader, r.extra)
			if err != nil {
				return 0, err
			}
		}
	}

	n := copy(bs, r.extra)
	r.extra = r.extra[n:]
	return n, nil
}

// ReadPacket decodes a packet from the reader.
// If no more to read then will return io.EOF as error.
func ReadPacket(r io.Reader) (*Packet, error) {
	bs := make([]byte, 6)
	_, err := io.ReadFull(r, bs)
	if err != nil {
		return nil, err
	}

	id := binary.BigEndian.Uint16(bs[0:2])
	len := int32(binary.BigEndian.Uint32(bs[2:6]))
	packet := &Packet{
		ID:  ID(id),
		Len: len,
	}

	if len > 0 {
		bs = make([]byte, len)
		_, err = io.ReadFull(r, bs)
		if err != nil {
			return nil, err
		}
		packet.Data = bs
	}

	return packet, nil
}

type ID uint16

type Packet struct {
	ID   ID
	Len  int32 // sign bit is used to indicate success or error.
	Data []byte
}

func (p *Packet) IsError() bool {
	return p.Len < 0
}

func (p *Packet) ErrorStatus() int {
	return int(-1 * p.Len)
}

func (p *Packet) BlobRequest() (id ID, d digest.Digest, err error) {
	id = p.ID
	d, err = digest.Parse(string(p.Data))
	return
}

func (p *Packet) Write(w io.Writer) (int, error) {
	bs := make([]byte, 6)
	binary.BigEndian.PutUint16(bs[0:2], uint16(p.ID))
	binary.BigEndian.PutUint32(bs[2:6], uint32(p.Len))

	_, err := w.Write(bs)
	if err != nil {
		return 0, err
	}

	if p.Len > 0 {
		return w.Write(p.Data)
	}

	return 0, nil
}

func BlobChunk(id ID, data []byte) *Packet {
	return &Packet{
		ID:   id,
		Len:  int32(len(data)),
		Data: data,
	}
}

func EOF(id ID) *Packet {
	return &Packet{
		ID:  id,
		Len: 0,
	}
}

func Error(id ID, httpStatus int) *Packet {
	return &Packet{
		ID:  id,
		Len: -1 * int32(httpStatus),
	}
}
