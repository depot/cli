package agent

import (
	"context"
	"strings"
	"sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// chatSession runs attach as an inline chat:
// the transcript prints above an input line that stays editable while output streams.
func chatSession(ctx context.Context, s *session, sessionID string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan string)
	queue := newLineQueue()
	go queue.forward(ctx, lines)
	p := tea.NewProgram(newChatModel(queue))
	transcript := &chatWriter{p: p}

	attachErr := make(chan error, 1)
	go func() {
		err := attachLines(ctx, s, sessionID, lines, nil, transcript)
		transcript.Flush()
		p.Quit()
		attachErr <- err
	}()

	if _, err := p.Run(); err != nil {
		cancel()
		return err
	}
	// The program also ends on its own, for example on a signal,
	// so stop attach rather than wait for a /quit that will never come.
	cancel()
	return <-attachErr
}

type chatPartialMsg string

type chatModel struct {
	input   textinput.Model
	queue   *lineQueue
	partial string
}

func newChatModel(queue *lineQueue) chatModel {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "message, /file <path>, /steer, /interrupt, /quit"
	input.Focus()
	return chatModel{input: input, queue: queue}
}

func (m chatModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatPartialMsg:
		m.partial = string(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.input.SetWidth(max(msg.Width-len(m.input.Prompt)-1, 1))
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			// Quitting the program cancels attach, even mid-upload.
			return m, tea.Quit
		case "enter":
			line := m.input.Value()
			m.input.Reset()
			if strings.TrimSpace(line) == "" {
				return m, nil
			}
			m.queue.push(line)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// lineQueue hands submitted lines to attach in order, without blocking the event loop.
type lineQueue struct {
	mu    sync.Mutex
	lines []string
	ready chan struct{}
}

func newLineQueue() *lineQueue {
	return &lineQueue{ready: make(chan struct{}, 1)}
}

func (q *lineQueue) push(line string) {
	q.mu.Lock()
	q.lines = append(q.lines, line)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// forward sends queued lines to out, oldest first, until ctx ends.
func (q *lineQueue) forward(ctx context.Context, out chan<- string) {
	for {
		q.mu.Lock()
		if len(q.lines) == 0 {
			q.mu.Unlock()
			select {
			case <-q.ready:
				continue
			case <-ctx.Done():
				return
			}
		}
		line := q.lines[0]
		q.lines = q.lines[1:]
		q.mu.Unlock()
		select {
		case out <- line:
		case <-ctx.Done():
			return
		}
	}
}

func (m chatModel) View() tea.View {
	if m.partial == "" {
		return tea.NewView(m.input.View())
	}
	return tea.NewView(m.partial + "\n" + m.input.View())
}

// chatWriter prints each complete line above the chat input, in order,
// and shows the incomplete last line, such as a streaming response, live above it.
type chatWriter struct {
	p   *tea.Program
	mu  sync.Mutex
	buf string
}

func (w *chatWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf += string(b)
	for {
		line, rest, ok := strings.Cut(w.buf, "\n")
		if !ok {
			break
		}
		printLine(w.p, line)
		w.buf = rest
	}
	w.p.Send(chatPartialMsg(w.buf))
	return len(b), nil
}

// Flush prints an incomplete last line so it is not lost when the chat ends.
func (w *chatWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf != "" {
		printLine(w.p, w.buf)
		w.buf = ""
		w.p.Send(chatPartialMsg(""))
	}
}

// printLine sends the print message straight into the program's ordered queue.
// Returning tea.Println from Update would run it on its own goroutine, which can reorder lines,
// and Program.Println blocks forever once the program has exited; Send does not.
func printLine(p *tea.Program, line string) {
	p.Send(tea.Println(line)())
}
