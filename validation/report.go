package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

type difference struct {
	Field     string `json:"field"`
	Baseline  string `json:"baseline"`
	Candidate string `json:"candidate"`
	Accepted  string `json:"accepted,omitempty"`
}

type scenarioResult struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	Failures    []string     `json:"failures,omitempty"`
	Differences []difference `json:"differences,omitempty"`
	Baseline    *observation `json:"baseline,omitempty"`
	Candidate   *observation `json:"candidate,omitempty"`
}

const (
	statusPass     = "PASS"
	statusDiffer   = "DIFFER"
	statusFail     = "FAIL"
	statusAccepted = "PASS (accepted differences)"
)

func significantLines(stderr string) string {
	var out []string
	for line := range strings.SplitSeq(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#N ") || strings.HasPrefix(trimmed, "#0 ") {
			continue
		}
		out = append(out, trimmed)
	}
	// Builds of separate projects run at the same time, so the order of
	// their messages is not fixed.
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func toJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func compareObservations(s scenario, base, cand *observation) []difference {
	var diffs []difference
	add := func(field, b, c string) {
		if b == c {
			return
		}
		d := difference{Field: field, Baseline: b, Candidate: c}
		if reason, ok := s.Accept[field]; ok {
			d.Accepted = reason
		}
		diffs = append(diffs, d)
	}
	add("exitCode", fmt.Sprint(base.ExitCode), fmt.Sprint(cand.ExitCode))
	add("stdout", base.Stdout, cand.Stdout)
	add("stderr", significantLines(base.Stderr), significantLines(cand.Stderr))
	for _, k := range sortedKeys(base.Files, cand.Files) {
		add("file:"+k, base.Files[k], cand.Files[k])
	}
	for _, k := range sortedKeys(base.Images, cand.Images) {
		add("image:"+k, toJSON(base.Images[k]), toJSON(cand.Images[k]))
	}
	for _, k := range sortedKeys(base.Registry, cand.Registry) {
		add("registry:"+k, toJSON(base.Registry[k]), toJSON(cand.Registry[k]))
	}
	add("api", base.APIJSON, cand.APIJSON)
	return diffs
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]struct{}{}
	for _, m := range maps {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkExpectations(s scenario, o *observation) []string {
	var failures []string
	e := s.Expect
	if o.ExitCode != e.ExitCode {
		failures = append(failures, fmt.Sprintf("exit code %d, expected %d", o.ExitCode, e.ExitCode))
	}
	for _, want := range e.StdoutContains {
		if !strings.Contains(o.Stdout, want) {
			failures = append(failures, fmt.Sprintf("stdout does not contain %q", want))
		}
	}
	for _, want := range e.StderrContains {
		if !strings.Contains(o.Stderr, want) {
			failures = append(failures, fmt.Sprintf("stderr does not contain %q", want))
		}
	}
	for _, unwanted := range e.StderrExcludes {
		if strings.Contains(o.Stderr, unwanted) {
			failures = append(failures, fmt.Sprintf("stderr contains %q", unwanted))
		}
	}
	if e.FinishBuild != nil && !slices.EqualFunc(o.API.FinishBuild, e.FinishBuild, matchPattern) {
		failures = append(failures, fmt.Sprintf("finish build results %q, expected %q", o.API.FinishBuild, e.FinishBuild))
	}
	if e.Check != nil {
		if err := e.Check(o); err != nil {
			failures = append(failures, err.Error())
		}
	}
	return failures
}

func evaluate(s scenario, base, cand *observation) scenarioResult {
	res := scenarioResult{Name: s.Name, Description: s.Description, Baseline: base, Candidate: cand}
	res.Failures = checkExpectations(s, cand)
	if base != nil {
		if s.BaselineDefect == "" {
			for _, f := range checkExpectations(s, base) {
				res.Failures = append(res.Failures, "baseline: "+f)
			}
		}
		res.Differences = compareObservations(s, base, cand)
		if s.BaselineDefect != "" {
			for i := range res.Differences {
				res.Differences[i].Accepted = "baseline defect: " + s.BaselineDefect
			}
		}
	}
	unaccepted := 0
	for _, d := range res.Differences {
		if d.Accepted == "" {
			unaccepted++
		}
	}
	switch {
	case len(res.Failures) > 0:
		res.Status = statusFail
	case unaccepted > 0:
		res.Status = statusDiffer
	case len(res.Differences) > 0:
		res.Status = statusAccepted
	default:
		res.Status = statusPass
	}
	return res
}

func writeSummary(w io.Writer, results []scenarioResult, verbose bool) {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
		fmt.Fprintf(w, "%-28s %s\n", r.Status, r.Name)
		for _, f := range r.Failures {
			fmt.Fprintf(w, "    failure: %s\n", f)
		}
		for _, d := range r.Differences {
			if d.Accepted != "" {
				fmt.Fprintf(w, "    accepted difference in %s: %s\n", d.Field, d.Accepted)
				continue
			}
			fmt.Fprintf(w, "    difference in %s\n", d.Field)
			if verbose {
				fmt.Fprintf(w, "%s\n", indent(unifiedDiff(d.Baseline, d.Candidate), "      "))
			}
		}
	}
	fmt.Fprintf(w, "\n%d scenarios: %d passed, %d passed with accepted differences, %d differ, %d failed\n",
		len(results), counts[statusPass], counts[statusAccepted], counts[statusDiffer], counts[statusFail])
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func unifiedDiff(a, b string) string {
	al := strings.Split(a, "\n")
	bl := strings.Split(b, "\n")
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			out = append(out, "  "+al[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+al[i])
			i++
		default:
			out = append(out, "+ "+bl[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+al[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+bl[j])
	}
	return strings.Join(out, "\n")
}

func matchPattern(value, pattern string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "<any>"); ok {
		return strings.HasPrefix(value, prefix)
	}
	return value == pattern
}
