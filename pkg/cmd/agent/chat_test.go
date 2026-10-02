package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestChatModelSubmitsLinesInOrderAndShowsThePartial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queue := newLineQueue()
	var m tea.Model = newChatModel(queue)

	m, _ = m.Update(chatPartialMsg("Thinking about"))
	for _, line := range []string{"one", "two", "three"} {
		for _, r := range line {
			m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		if line == "one" {
			if view := m.View().Content; !strings.Contains(view, "Thinking about\n") || !strings.Contains(view, "one") {
				t.Fatalf("view should show the partial above the typed line, got:\n%s", view)
			}
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.(chatModel).input.Value() != "" {
			t.Fatal("enter should clear the input")
		}
	}

	// Lines submitted while attach was busy still arrive in the order they were typed.
	lines := make(chan string)
	go queue.forward(ctx, lines)
	for _, want := range []string{"one", "two", "three"} {
		if got := <-lines; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestChatModelQuitsDirectlyOnCtrlCAndCtrlD(t *testing.T) {
	for _, key := range []rune{'c', 'd'} {
		_, cmd := newChatModel(newLineQueue()).Update(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl})
		if cmd == nil {
			t.Fatalf("ctrl+%c should quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("ctrl+%c should quit the program, so a busy attach is cancelled", key)
		}
	}
}
