package helpers

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
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

	return password, nil
}

func stripANSI(s string) string {
	// Matches ESC followed by bracket and any sequence of characters ending in a letter
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(s, "")
}

func PromptForValue(prompt string) (string, error) {
	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	fmt.Println()

	return strings.TrimSpace(input), nil
}
