package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestChatModelSubmitsTheLineAndShowsThePartial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	submitted := make(chan string, 1)
	var m tea.Model = newChatModel(ctx, submitted)

	m, _ = m.Update(chatPartialMsg("Thinking about"))
	for _, r := range "hi" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if view := m.View().Content; !strings.Contains(view, "Thinking about\n") || !strings.Contains(view, "hi") {
		t.Fatalf("view should show the partial above the typed line, got:\n%s", view)
	}

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should return a command that submits the line")
	}
	cmd()
	if got := <-submitted; got != "hi" {
		t.Fatalf("submitted %q, want %q", got, "hi")
	}
	if m.(chatModel).input.Value() != "" {
		t.Fatal("enter should clear the input")
	}
}

func TestChatModelQuitsOnCtrlD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	submitted := make(chan string, 1)
	_, cmd := newChatModel(ctx, submitted).Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	cmd()
	if got := <-submitted; got != "/quit" {
		t.Fatalf("ctrl+d submitted %q, want /quit", got)
	}
}
