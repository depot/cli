package tests

import (
	"bufio"
	"io"
	"os"
	"strings"
)

func loadCandidates(r io.Reader, candidatesFile string) ([]string, error) {
	if candidatesFile != "" {
		file, err := os.Open(candidatesFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return readCandidates(file)
	}

	return readCandidates(r)
}

func readCandidates(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var candidates []string
	for scanner.Scan() {
		candidate := strings.TrimSpace(scanner.Text())
		if candidate == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}
