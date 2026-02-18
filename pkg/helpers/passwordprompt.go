package helpers

import (
	"fmt"
	"regexp"
	"syscall"

	"golang.org/x/term"
)

func PromptForSecret(prompt string) (string, error) {
	fmt.Print(prompt)

	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	password := stripANSI(string(bytePassword))
	fmt.Println()

	return string(password), nil
}

func stripANSI(s string) string {
	// Matches ESC followed by bracket and any sequence of characters ending in a letter
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(s, "")
}
