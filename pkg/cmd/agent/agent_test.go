package agent

import "testing"

func TestAgentIsHiddenUntilItShipsToEveryone(t *testing.T) {
	if !NewCmdAgent().Hidden {
		t.Fatal("depot agent must stay out of help and completions until it is announced (DEP-7790)")
	}
}
