package gocache

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/depot/cli/pkg/cmd/gocache/wire"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

const CacheServer = "https://cache.depot.dev"

func NewCmdGoCache() *cobra.Command {
	var (
		verbose bool
		token   string
		orgID   string
		dir     string
	)
	cmd := &cobra.Command{
		Use:   "gocache",
		Short: `Go compiler remote cache using Depot. To use set GOCACHEPROG="depot gocache"`,
		Long:  "depot gocache implements the Go compiler external cache protocol. It communicates over stdin/stdout with the Go tool cache.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			err := os.MkdirAll(dir, 0755)
			if err != nil {
				return err
			}

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			p := NewCache(CacheServer, orgID, token, dir, verbose)
			return p.Run(ctx)
		},
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.BoolVarP(&verbose, "verbose", "v", false, "Print verbose output")
	flags.StringVar(&token, "token", "", "Depot token")
	flags.StringVarP(&orgID, "organization", "o", "", "Depot organization ID")
	flags.StringVar(&dir, "dir", defaultCacheDir(), "Directory to store cache files")

	return cmd
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir = filepath.Join(dir, "depot-go-cache")
	return dir
}

// Cache implements the cmd/go JSON protocol over stdin & stdout via three
// funcs that callers can optionally implement.
type Cache struct {
	RemoteCache *RemoteCache
	Verbose     bool

	Gets      atomic.Int64
	GetHits   atomic.Int64
	GetMisses atomic.Int64
	GetErrors atomic.Int64
	Puts      atomic.Int64
	PutErrors atomic.Int64
}

func NewCache(baseURL, orgID, token, dir string, verbose bool) *Cache {
	disk := &DiskCache{Dir: dir, Verbose: verbose}
	hc := &RemoteCache{
		BaseURL: baseURL,
		Token:   token,
		OrgID:   orgID,
		Disk:    disk,
		Verbose: verbose,
	}
	// Background because we use .Close() to handle shutdown of the of the background PUT operations.
	hc.Ctx, hc.CtxCancel = context.WithCancel(context.Background())

	p := &Cache{
		RemoteCache: hc,
		Verbose:     verbose,
	}
	return p
}

func (p *Cache) Run(ctx context.Context) error {
	defer p.RemoteCache.Close()

	br := bufio.NewReader(os.Stdin)
	dec := json.NewDecoder(br)

	bw := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(bw)

	caps := []wire.ProgCmd{"get", "put", "close"}
	_ = enc.Encode(&wire.ProgResponse{KnownCommands: caps})
	err := bw.Flush()
	if err != nil {
		return err
	}

	var wmu sync.Mutex // guards writing responses
	for {
		var req wire.ProgRequest
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		// The content of a PUT immediately follows the command.
		// The content is encoded as a JSON base64 encoded string.
		if req.Command == wire.CmdPut && req.BodySize > 0 {
			var bodyb []byte
			if err := dec.Decode(&bodyb); err != nil {
				log.Fatal(err)
			}
			if int64(len(bodyb)) != req.BodySize {
				log.Fatalf("only got %d bytes of declared %d", len(bodyb), req.BodySize)
			}
			req.Body = bytes.NewReader(bodyb)
		}

		// Handle the request in a goroutine so we can handle multiple requests concurrently.
		// The request ID is used to match responses to requests within the compiler.
		go func(ctx context.Context) {
			res := &wire.ProgResponse{ID: req.ID}
			if err := p.handleRequest(ctx, &req, res); err != nil {
				res.Err = err.Error()
			}
			wmu.Lock()
			defer wmu.Unlock()
			_ = enc.Encode(res)
			_ = bw.Flush()
		}(ctx)
	}
}

func (p *Cache) handleRequest(ctx context.Context, req *wire.ProgRequest, res *wire.ProgResponse) error {
	switch req.Command {
	default:
		return errors.New("unknown command")
	case "close":
		// Close will wait up to 10 seconds for all operations to finish.
		_ = p.RemoteCache.Close()
		if p.Verbose {
			log.Printf("cacher: closing; %d gets (%d hits, %d misses, %d errors); %d puts (%d errors)",
				p.Gets.Load(), p.GetHits.Load(), p.GetMisses.Load(), p.GetErrors.Load(), p.Puts.Load(), p.PutErrors.Load())
		}
		return nil
	case "get":
		return p.handleGet(ctx, req, res)
	case "put":
		return p.handlePut(ctx, req, res)
	}
}

func (p *Cache) handleGet(ctx context.Context, req *wire.ProgRequest, res *wire.ProgResponse) (retErr error) {
	p.Gets.Add(1)
	defer func() {
		if retErr != nil {
			log.Printf("get(action %x): %v", req.ActionID, retErr)
			p.GetErrors.Add(1)
		} else if res.Miss {
			p.GetMisses.Add(1)
		} else {
			p.GetHits.Add(1)
		}
	}()
	outputID, diskPath, err := p.RemoteCache.Get(ctx, fmt.Sprintf("%x", req.ActionID))
	if err != nil {
		return err
	}
	if outputID == "" && diskPath == "" {
		res.Miss = true
		return nil
	}
	if outputID == "" {
		return errors.New("no outputID")
	}
	res.OutputID, err = hex.DecodeString(outputID)
	if err != nil {
		return fmt.Errorf("invalid OutputID: %v", err)
	}
	fi, err := os.Stat(diskPath)
	if err != nil {
		if os.IsNotExist(err) {
			res.Miss = true
			return nil
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	res.Size = fi.Size()
	time := fi.ModTime()
	res.Time = &time
	res.DiskPath = diskPath
	return nil
}

func (p *Cache) handlePut(ctx context.Context, req *wire.ProgRequest, res *wire.ProgResponse) (retErr error) {
	actionID, objectID := fmt.Sprintf("%x", req.ActionID), fmt.Sprintf("%x", req.OutputID)
	p.Puts.Add(1)
	defer func() {
		if retErr != nil {
			p.PutErrors.Add(1)
			log.Printf("put(action %s, obj %s, %v bytes): %v", actionID, objectID, req.BodySize, retErr)
		}
	}()

	if req.OutputID == nil && req.ObjectID != nil {
		req.OutputID = req.ObjectID
	}
	if req.OutputID == nil && req.ObjectID == nil {
		return fmt.Errorf("missing OutputID")
	}

	var body io.Reader = req.Body
	if body == nil {
		body = bytes.NewReader(nil)
	}
	diskPath, err := p.RemoteCache.Put(ctx, actionID, objectID, req.BodySize, body)
	if err != nil {
		return err
	}
	fi, err := os.Stat(diskPath)
	if err != nil {
		return fmt.Errorf("stat after successful Put: %w", err)
	}
	if fi.Size() != req.BodySize {
		return fmt.Errorf("failed to write file to disk with right size: disk=%v; wanted=%v", fi.Size(), req.BodySize)
	}
	res.DiskPath = diskPath
	return nil
}

type RemoteCache struct {
	// BaseURL is the base URL of the cacher server, like "http://localhost:31364".
	BaseURL string

	// OrgID is the optional Depot org id used for user tokens.
	OrgID string
	// Token is the optional Depot token used for user tokens.
	Token string

	// Disk is where to write the output files to local disk, as required by the
	// cache protocol.
	Disk *DiskCache

	// HTTPClient optionally specifies the http.Client to use.
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	Verbose bool

	// Ctx is the context for all background PUT operations.
	// Valid until Close.
	Ctx context.Context
	// CtxCancel cancels the context for all background PUT operations.
	CtxCancel context.CancelFunc

	wg sync.WaitGroup
}

// Close cancels all background PUT operations.
// It waits up to 10 seconds for all operations to finish.
func (c *RemoteCache) Close() error {
	go func() {
		time.Sleep(10 * time.Second)
		c.CtxCancel()
	}()
	c.wg.Wait()
	return nil
}

func (c *RemoteCache) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *RemoteCache) Get(ctx context.Context, actionID string) (outputID, diskPath string, err error) {
	outputID, diskPath, err = c.Disk.Get(ctx, actionID)
	if err == nil && outputID != "" {
		return outputID, diskPath, nil
	}

	now := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/gocache/v1/"+actionID, nil)
	req.Header.Set("User-Agent", "gocacheprog")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.OrgID != "" {
		req.Header.Set("X-Depot-Org", c.OrgID)
	}

	res, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return "", "", nil
	}
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected GET /gocache/v1/%s status %v", outputID, res.Status)
	}

	var size uint32
	if res.Header.Get("Content-Length") == "0" {
		outputID = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256 of empty string
	} else {
		// Read the length of the outputID and then the outputID itself.
		b := make([]byte, 1)
		_, err = io.ReadAtLeast(io.LimitReader(res.Body, 1), b, 1)
		if err != nil {
			return "", "", fmt.Errorf("unable to read outputID length: %v", err)
		}
		outputIDLen := int64(b[0])

		outputIDBuf := make([]byte, outputIDLen)
		_, err = io.ReadAtLeast(io.LimitReader(res.Body, outputIDLen), outputIDBuf, int(outputIDLen))
		if err != nil {
			return "", "", fmt.Errorf("unable to read outputID: %v", err)
		}
		outputID = string(outputIDBuf)

		err = binary.Read(res.Body, binary.LittleEndian, &size)
		if err != nil {
			return "", "", fmt.Errorf("unable to read size: %v", err)
		}
	}

	if c.Verbose {
		dur := time.Since(now)
		log.Printf("GET /gocache/v1/%s/%s: %d bytes in %v", actionID, outputID, size, dur)
	}

	// The rest of the body is the actual output.
	now = time.Now()
	diskPath, err = c.Disk.Put(ctx, actionID, outputID, int64(size), res.Body)
	if err != nil {
		return "", "", err
	}
	if c.Verbose {
		dur := time.Since(now)
		log.Printf("CACHED %s: %d bytes in %v", actionID, size, dur)
	}
	return outputID, diskPath, err
}

func (c *RemoteCache) Put(ctx context.Context, actionID, outputID string, size int64, body io.Reader) (diskPath string, _ error) {
	if size < 0 {
		return "", fmt.Errorf("negative size %d", size)
	}
	if size >= 4<<30 { // 4GB
		return "", fmt.Errorf("size %d too large", size)
	}
	if len(outputID) > 255 {
		return "", fmt.Errorf("outputID too long: %d", len(outputID))
	}

	// Header is 1 byte for the length of the outputID, the outputID itself, and 4 bytes for the size.
	headerSize := 1 + len(outputID) + 4
	b := bytes.NewBuffer(make([]byte, 0, headerSize+int(size)))
	b.WriteByte(byte(len(outputID)))
	b.WriteString(outputID)
	_ = binary.Write(b, binary.LittleEndian, uint32(size))
	_, err := io.Copy(b, body)
	if err != nil {
		return "", err
	}
	buf := b.Bytes()

	diskPath, err = c.Disk.Put(ctx, actionID, outputID, size, bytes.NewReader(buf[headerSize:]))
	if err != nil {
		return "", err
	}

	if len(outputID) == 0 {
		return diskPath, nil
	}

	// Send the output to the cache server in the background.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		putBody := bytes.NewReader(buf)
		if size == 0 {
			putBody = bytes.NewReader(nil)
		}
		req, _ := http.NewRequestWithContext(c.Ctx, "PUT", c.BaseURL+"/gocache/v1/"+actionID, putBody)
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}

		req.Header.Set("User-Agent", "gocacheprog")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if c.OrgID != "" {
			req.Header.Set("X-Depot-Org", c.OrgID)
		}

		req.ContentLength = int64(len(buf))
		res, err := c.httpClient().Do(req)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("error PUT /%s/%s: %v", actionID, outputID, err)
			}
			return
		}

		defer res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			all, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
			log.Printf("unexpected PUT /gocache/v1/%s/%s status %v: %s", actionID, outputID, res.Status, all)
			return
		}
	}()

	return diskPath, nil
}

// indexEntry is the metadata that DiskCache stores on disk for an ActionID.
type indexEntry struct {
	Version   int    `json:"v"`
	OutputID  string `json:"o"`
	Size      int64  `json:"n"`
	TimeNanos int64  `json:"t"`
}

type DiskCache struct {
	Dir     string
	Verbose bool
}

func (dc *DiskCache) Get(ctx context.Context, actionID string) (outputID, diskPath string, err error) {
	actionFile := filepath.Join(dc.Dir, fmt.Sprintf("a-%s", actionID))
	ij, err := os.ReadFile(actionFile)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
			if dc.Verbose {
				log.Printf("disk miss: %v", actionID)
			}
		}
		return "", "", err
	}
	var ie indexEntry
	if err := json.Unmarshal(ij, &ie); err != nil {
		log.Printf("Warning: JSON error for action %q: %v", actionID, err)
		return "", "", nil
	}
	if _, err := hex.DecodeString(ie.OutputID); err != nil {
		// Protect against malicious non-hex OutputID on disk
		return "", "", nil
	}
	return ie.OutputID, filepath.Join(dc.Dir, fmt.Sprintf("o-%v", ie.OutputID)), nil
}

func (dc *DiskCache) OutputFilename(objectID string) string {
	if len(objectID) < 4 || len(objectID) > 1000 {
		return ""
	}
	for i := range objectID {
		b := objectID[i]
		if b >= '0' && b <= '9' || b >= 'a' && b <= 'f' {
			continue
		}
		return ""
	}
	return filepath.Join(dc.Dir, fmt.Sprintf("o-%s", objectID))
}

func (dc *DiskCache) Put(ctx context.Context, actionID, objectID string, size int64, body io.Reader) (diskPath string, _ error) {
	file := filepath.Join(dc.Dir, fmt.Sprintf("o-%s", objectID))

	// Special case empty files; they're both common and easier to do race-free.
	if size == 0 {
		zf, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return "", err
		}
		zf.Close()
	} else {
		wrote, err := writeAtomic(file, body)
		if err != nil {
			return "", err
		}
		if wrote != size {
			return "", fmt.Errorf("wrote %d bytes, expected %d", wrote, size)
		}
	}

	ij, err := json.Marshal(indexEntry{
		Version:   1,
		OutputID:  objectID,
		Size:      size,
		TimeNanos: time.Now().UnixNano(),
	})
	if err != nil {
		return "", err
	}
	actionFile := filepath.Join(dc.Dir, fmt.Sprintf("a-%s", actionID))
	if _, err := writeAtomic(actionFile, bytes.NewReader(ij)); err != nil {
		return "", err
	}
	return file, nil
}

func writeAtomic(dest string, r io.Reader) (int64, error) {
	tf, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*")
	if err != nil {
		return 0, err
	}
	size, err := io.Copy(tf, r)
	if err != nil {
		tf.Close()
		os.Remove(tf.Name())
		return 0, err
	}
	if err := tf.Close(); err != nil {
		os.Remove(tf.Name())
		return 0, err
	}
	if err := os.Rename(tf.Name(), dest); err != nil {
		os.Remove(tf.Name())
		return 0, err
	}
	return size, nil
}
