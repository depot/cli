package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sync"
	"time"
	"unicode/utf16"

	"connectrpc.com/connect"
	"github.com/depot/cli/internal/build"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The limits Depot puts on an offer.
// A server past them is trimmed here, with a notice, rather than refused whole.
const (
	localMcpMaxServers         = 20
	localMcpMaxTools           = 50
	localMcpMaxDescription     = 200
	localMcpMaxToolDescription = 1000
	localMcpMaxToolName        = 128
	// Depot measures every server's offer together, as JSON; this is measured the same way.
	localMcpMaxOfferBytes = 256 * 1024
	localMcpStartTimeout  = 30 * time.Second
)

var localMcpServerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

// mcpConfigFile is the `mcpServers` format other MCP clients read from .mcp.json.
type mcpConfigFile struct {
	McpServers map[string]mcpServerConfig `json:"mcpServers"`
}

type mcpServerConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
}

type namedMcpServer struct {
	name string
	mcpServerConfig
}

// loadMcpConfig reads the stdio servers of an MCP config file, in name order,
// expanding environment variables in their command, arguments and env.
// It skips remote servers, returning their names:
// Depot can reach those itself, as organization servers.
func loadMcpConfig(path string) (servers []namedMcpServer, remote []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read MCP config: %w", err)
	}
	var file mcpConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, fmt.Errorf("parse MCP config %s: %w", path, err)
	}
	for _, name := range slices.Sorted(maps.Keys(file.McpServers)) {
		c := file.McpServers[name]
		if c.Command == "" || (c.Type != "" && c.Type != "stdio") {
			remote = append(remote, name)
			continue
		}
		if !localMcpServerName.MatchString(name) {
			return nil, nil, fmt.Errorf("MCP server name %q must be 1-20 lowercase letters, digits or dashes, starting with a letter", name)
		}
		c.Command = os.ExpandEnv(c.Command)
		args := make([]string, len(c.Args))
		for i, arg := range c.Args {
			args[i] = os.ExpandEnv(arg)
		}
		c.Args = args
		env := make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			env[k] = os.ExpandEnv(v)
		}
		c.Env = env
		servers = append(servers, namedMcpServer{name: name, mcpServerConfig: c})
	}
	if len(servers) > localMcpMaxServers {
		return nil, nil, fmt.Errorf("MCP config %s has %d stdio servers; Depot takes at most %d", path, len(servers), localMcpMaxServers)
	}
	return servers, remote, nil
}

// localMcp is the MCP servers one attach runs and offers to its session.
type localMcp struct {
	stop     context.CancelFunc
	sessions map[string]*mcp.ClientSession
	offers   []*agentv1.LocalMcpServer
}

// startLocalMcp starts each configured server and lists its tools.
// A server that fails to start is left out with a notice,
// so one broken entry does not keep the rest from the agent.
func startLocalMcp(ctx context.Context, configs []namedMcpServer, notices io.Writer) *localMcp {
	// The SDK ties each server's connection to this context, so it lives as long as the servers do.
	ctx, stop := context.WithCancel(ctx)
	l := &localMcp{stop: stop, sessions: map[string]*mcp.ClientSession{}}
	client := mcp.NewClient(&mcp.Implementation{Name: "depot-cli", Version: build.Version}, nil)
	budget := localMcpMaxOfferBytes
	for _, c := range configs {
		cs, offer, err := startServer(ctx, client, c, &budget, notices)
		if err != nil {
			fmt.Fprintf(notices, "(local MCP server %s is not offered: %v)\n", c.name, err)
			continue
		}
		l.sessions[c.name] = cs
		l.offers = append(l.offers, offer)
	}
	return l
}

func startServer(ctx context.Context, client *mcp.Client, c namedMcpServer, budget *int, notices io.Writer) (*mcp.ClientSession, *agentv1.LocalMcpServer, error) {
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A server's own stderr would garble the chat, so it is dropped.
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(localMcpStartTimeout, cancel)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start: %w", err)
	}
	offer, err := offerFor(ctx, c.name, cs, budget, notices)
	if !timer.Stop() {
		err = cmp.Or(err, fmt.Errorf("did not list its tools within %s", localMcpStartTimeout))
	}
	if err != nil {
		_ = cs.Close()
		cancel()
		return nil, nil, err
	}
	return cs, offer, nil
}

func offerFor(ctx context.Context, name string, cs *mcp.ClientSession, budget *int, notices io.Writer) (*agentv1.LocalMcpServer, error) {
	offer := &agentv1.LocalMcpServer{Name: name}
	if init := cs.InitializeResult(); init != nil {
		description := init.Instructions
		if info := init.ServerInfo; info != nil {
			description = cmp.Or(info.Description, description, info.Title, info.Name)
		}
		offer.Description = truncateUTF16(description, localMcpMaxDescription)
	}
	*budget -= jsonSize(offer)
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		if tool.Name == "" || len(utf16.Encode([]rune(tool.Name))) > localMcpMaxToolName {
			fmt.Fprintf(notices, "(local MCP server %s: skipping a tool whose name is empty or over %d characters)\n", name, localMcpMaxToolName)
			continue
		}
		if len(offer.Tools) == localMcpMaxTools {
			fmt.Fprintf(notices, "(local MCP server %s: offering only its first %d tools)\n", name, localMcpMaxTools)
			break
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("encode the input schema of tool %s: %w", tool.Name, err)
		}
		offered := &agentv1.LocalMcpTool{
			Name:            tool.Name,
			Description:     truncateUTF16(tool.Description, localMcpMaxToolDescription),
			InputSchemaJson: string(schema),
		}
		size := jsonSize(offered)
		if size > *budget {
			fmt.Fprintf(notices, "(local MCP server %s: skipping tool %s, past Depot's %d KB limit on all servers' tools)\n", name, tool.Name, localMcpMaxOfferBytes/1024)
			continue
		}
		*budget -= size
		offer.Tools = append(offer.Tools, offered)
	}
	return offer, nil
}

// jsonSize overestimates a little, since Go escapes more characters than Depot does.
func jsonSize(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

// truncateUTF16 cuts s to at most max UTF-16 code units, the unit Depot counts characters in.
func truncateUTF16(s string, max int) string {
	n := 0
	for i, r := range s {
		n += utf16.RuneLen(r)
		if n > max {
			return s[:i]
		}
	}
	return s
}

// Close stops every server, in parallel, since each may take seconds to exit.
func (l *localMcp) Close() {
	var wg sync.WaitGroup
	for _, cs := range l.sessions {
		wg.Go(func() { _ = cs.Close() })
	}
	wg.Wait()
	l.stop()
}

// serve offers the servers to the session and runs the calls the agent sends them.
// It reattaches whenever the stream ends, until ctx ends,
// a newer attach replaces this one, or Depot refuses the offer.
func (l *localMcp) serve(ctx context.Context, s *session, sessionID string, notices io.Writer) {
	if len(l.offers) == 0 {
		return
	}
	var calls sync.WaitGroup
	defer calls.Wait()
	announced := false
	attached := func() {
		if !announced {
			announced = true
			fmt.Fprintf(notices, "(offering %d local MCP servers to the agent)\n", len(l.offers))
		}
	}
	for failures := 0; ; {
		err := l.pull(ctx, s, sessionID, &calls, attached)
		if ctx.Err() != nil {
			return
		}
		var connectErr *connect.Error
		if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeFailedPrecondition {
			fmt.Fprintln(notices, "(another attach now offers its local MCP servers to this session; yours are no longer offered)")
			return
		}
		if err != nil && !retryable(err) {
			fmt.Fprintf(notices, "(local MCP servers are no longer offered: %v)\n", err)
			return
		}
		if err == nil {
			failures = 0
			continue
		}
		failures++
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(min(failures, 5)) * time.Second):
		}
	}
}

// pull holds one stream, running each call it delivers and answering it.
// Calls still running when the stream ends are cancelled: Depot has already failed them.
func (l *localMcp) pull(ctx context.Context, s *session, sessionID string, calls *sync.WaitGroup, attached func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.client.PullLocalMcpCalls(ctx, authed(s, &agentv1.PullLocalMcpCallsRequest{SessionId: sessionID, Servers: l.offers}))
	if err != nil {
		return err
	}
	defer stream.Close()

	var mu sync.Mutex
	running := map[string]context.CancelFunc{}
	var attachmentID string
	for stream.Receive() {
		switch event := stream.Msg().GetCall().(type) {
		case *agentv1.PullLocalMcpCallsResponse_Attached:
			attachmentID = event.Attached.GetAttachmentId()
			attached()
		case *agentv1.PullLocalMcpCallsResponse_Cancel:
			mu.Lock()
			if stop := running[event.Cancel.GetCallId()]; stop != nil {
				stop()
			}
			mu.Unlock()
		case *agentv1.PullLocalMcpCallsResponse_CallTool:
			call := event.CallTool
			callCtx, stop := context.WithCancel(ctx)
			mu.Lock()
			running[call.GetCallId()] = stop
			mu.Unlock()
			respond := &agentv1.RespondLocalMcpCallRequest{SessionId: sessionID, AttachmentId: attachmentID, CallId: call.GetCallId()}
			calls.Go(func() {
				defer func() {
					mu.Lock()
					delete(running, call.GetCallId())
					mu.Unlock()
					stop()
				}()
				respond.Result = l.call(callCtx, call)
				if callCtx.Err() != nil {
					return
				}
				// A failed answer reaches the agent as a timeout; there is no one else to tell.
				_, _ = withRetries(callCtx, func() (*connect.Response[agentv1.RespondLocalMcpCallResponse], error) {
					return s.client.RespondLocalMcpCall(callCtx, authed(s, respond))
				})
			})
		}
	}
	return stream.Err()
}

// call runs one tool call. Every failure, including a server that has exited, is an error result for the agent.
func (l *localMcp) call(ctx context.Context, call *agentv1.LocalMcpToolCall) *agentv1.LocalMcpToolResult {
	cs := l.sessions[call.GetServerName()]
	if cs == nil {
		return localMcpError(fmt.Sprintf("no local MCP server is named %q", call.GetServerName()))
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      call.GetToolName(),
		Arguments: json.RawMessage(cmp.Or(call.GetArgumentsJson(), "{}")),
	})
	if err != nil {
		return localMcpError(fmt.Sprintf("local MCP server %s failed: %v", call.GetServerName(), err))
	}
	content := res.Content
	if len(content) == 0 && res.StructuredContent != nil {
		structured, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return localMcpError(fmt.Sprintf("encode the structured result: %v", err))
		}
		content = []mcp.Content{&mcp.TextContent{Text: string(structured)}}
	}
	if content == nil {
		content = []mcp.Content{}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return localMcpError(fmt.Sprintf("encode the result: %v", err))
	}
	return &agentv1.LocalMcpToolResult{ContentJson: string(encoded), IsError: res.IsError}
}

func localMcpError(text string) *agentv1.LocalMcpToolResult {
	encoded, _ := json.Marshal([]mcp.Content{&mcp.TextContent{Text: text}})
	return &agentv1.LocalMcpToolResult{ContentJson: string(encoded), IsError: true}
}
