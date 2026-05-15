package helpers

import (
	"bufio"
	"fmt"
	"io"
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

func SecretValueFromInput(prompt string) (string, error) {
	if !IsStdinTerminal() {
		return SecretValueFromStdin()
	}
	return PromptForSecret(prompt)
}

func SecretValueFromStdin() (string, error) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	if len(input) == 0 {
		return "", fmt.Errorf("no secret value provided on stdin")
	}
	return stripANSI(trimOneTrailingNewline(string(input))), nil
}

func trimOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return strings.TrimSuffix(s, "\r\n")
	}
	return strings.TrimSuffix(s, "\n")
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

func PromptForYN(prompt string) (bool, error) {
	val, err := PromptForValue(prompt)
	if err != nil {
		return false, err
	}
	val = strings.ToLower(val)
	return val == "y" || val == "yes", nil
}
