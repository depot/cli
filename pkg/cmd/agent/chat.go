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
	p := tea.NewProgram(newChatModel(ctx, lines))
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
	ctx     context.Context
	input   textinput.Model
	submit  chan<- string
	partial string
}

func newChatModel(ctx context.Context, submit chan<- string) chatModel {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "message, /file <path>, /steer, /interrupt, /quit"
	input.Focus()
	return chatModel{ctx: ctx, input: input, submit: submit}
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
			return m, m.send("/quit")
		case "enter":
			line := m.input.Value()
			m.input.Reset()
			if strings.TrimSpace(line) == "" {
				return m, nil
			}
			return m, m.send(line)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// send hands a line to attach without blocking the event loop.
func (m chatModel) send(line string) tea.Cmd {
	return func() tea.Msg {
		select {
		case m.submit <- line:
		case <-m.ctx.Done():
		}
		return nil
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
