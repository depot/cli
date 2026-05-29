package ci

import (
	"fmt"
	"io"
	"strings"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

type JobCandidate struct {
	Job          *civ1.JobStatus
	WorkflowPath string
	WorkflowName string
}

type JobPickerItem struct {
	Name     string
	Status   string
	Workflow string
	Index    int
}

type ResolveAttemptOptions struct {
	OriginalID     string
	JobKey         string
	WorkflowFilter string
	AllowPicker    bool
	Picker         func([]JobPickerItem) (int, error)
	InfoWriter     io.Writer
}

type JobSelectionOptions struct {
	AllowPicker bool
	Picker      func([]JobPickerItem) (int, error)
}

func ResolveAttemptForRunStatus(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string) (string, error) {
	return ResolveAttempt(resp, ResolveAttemptOptions{
		OriginalID:     originalID,
		JobKey:         jobKey,
		WorkflowFilter: workflowFilter,
	})
}

func ResolveAttempt(resp *civ1.GetRunStatusResponse, opts ResolveAttemptOptions) (string, error) {
	targetJob, workflowPath, err := FindJobForAttempt(resp, opts.OriginalID, opts.JobKey, opts.WorkflowFilter, JobSelectionOptions{
		AllowPicker: opts.AllowPicker,
		Picker:      opts.Picker,
	})
	if err != nil {
		return "", err
	}

	if len(targetJob.Attempts) == 0 {
		return "", fmt.Errorf("job %q has no attempts yet", targetJob.JobKey)
	}

	latest := targetJob.Attempts[0]
	for _, a := range targetJob.Attempts[1:] {
		if a.Attempt > latest.Attempt {
			latest = a
		}
	}

	var info []string
	if opts.JobKey == "" {
		if workflowPath != "" {
			info = append(info, fmt.Sprintf("job %q from %s", JobKeyShort(targetJob.JobKey), workflowPath))
		} else {
			info = append(info, fmt.Sprintf("job %q", JobKeyShort(targetJob.JobKey)))
		}
	}

	if len(targetJob.Attempts) > 1 {
		var others []string
		for _, a := range targetJob.Attempts {
			if a.AttemptId != latest.AttemptId {
				others = append(others, fmt.Sprintf("#%d %s", a.Attempt, a.AttemptId))
			}
		}
		info = append(info, fmt.Sprintf("attempt #%d (also available: %s)", latest.Attempt, strings.Join(others, ", ")))
	}

	if len(info) > 0 && opts.InfoWriter != nil {
		fmt.Fprintf(opts.InfoWriter, "Using %s\n", strings.Join(info, ", "))
	}

	return latest.AttemptId, nil
}

func FindJobForAttempt(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string, opts JobSelectionOptions) (*civ1.JobStatus, string, error) {
	var candidates []JobCandidate
	for _, wf := range resp.Workflows {
		if workflowFilter != "" && !WorkflowPathMatches(wf.WorkflowPath, workflowFilter) {
			continue
		}
		for _, j := range wf.Jobs {
			candidates = append(candidates, JobCandidate{
				Job:          j,
				WorkflowPath: wf.WorkflowPath,
				WorkflowName: wf.Name,
			})
		}
	}

	if len(candidates) == 0 {
		if workflowFilter != "" {
			return nil, "", fmt.Errorf("no jobs found in workflow %q", workflowFilter)
		}
		return nil, "", fmt.Errorf("run %s has no jobs", resp.RunId)
	}

	if jobKey != "" {
		bestTier := 0
		tierMatches := map[int][]JobCandidate{}
		for _, c := range candidates {
			if tier := MatchJobKey(c.Job.JobKey, jobKey); tier > 0 {
				tierMatches[tier] = append(tierMatches[tier], c)
				if bestTier == 0 || tier < bestTier {
					bestTier = tier
				}
			}
		}

		matches := tierMatches[bestTier]

		displayNames := JobDisplayNames(candidates)
		switch len(matches) {
		case 0:
			keys := make([]string, len(candidates))
			for i, c := range candidates {
				keys[i] = displayNames[c.Job.JobKey]
			}
			return nil, "", fmt.Errorf("job %q not found (available: %s)", jobKey, strings.Join(keys, ", "))
		case 1:
			return matches[0].Job, matches[0].WorkflowPath, nil
		default:
			uniquePaths := map[string]struct{}{}
			for _, m := range matches {
				uniquePaths[m.WorkflowPath] = struct{}{}
			}
			if len(uniquePaths) > 1 {
				paths := make([]string, 0, len(uniquePaths))
				for path := range uniquePaths {
					paths = append(paths, path)
				}
				return nil, "", fmt.Errorf("job %q exists in multiple workflows, specify one with --workflow: %s", jobKey, strings.Join(paths, ", "))
			}
			keys := make([]string, len(matches))
			for i, m := range matches {
				keys[i] = displayNames[m.Job.JobKey]
			}
			return nil, "", fmt.Errorf("job %q matches multiple jobs, use a more specific --job value: %s", jobKey, strings.Join(keys, ", "))
		}
	}

	for _, c := range candidates {
		if c.Job.JobId == originalID {
			return c.Job, c.WorkflowPath, nil
		}
	}

	if len(candidates) == 1 {
		return candidates[0].Job, candidates[0].WorkflowPath, nil
	}

	if opts.AllowPicker && opts.Picker != nil {
		displayNames := JobDisplayNames(candidates)
		items := make([]JobPickerItem, len(candidates))
		for i, c := range candidates {
			items[i] = JobPickerItem{
				Name:     displayNames[c.Job.JobKey],
				Status:   c.Job.Status,
				Workflow: c.WorkflowPath,
				Index:    i,
			}
		}
		idx, err := opts.Picker(items)
		if err != nil {
			return nil, "", err
		}
		if idx < 0 || idx >= len(candidates) {
			return nil, "", fmt.Errorf("selected job index %d out of range", idx)
		}
		return candidates[idx].Job, candidates[idx].WorkflowPath, nil
	}

	return nil, "", fmt.Errorf("run has multiple jobs, specify one with --job:\n%s", FormatJobList(candidates))
}

func JobKeyShort(key string) string {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[i+1:]
	}
	return key
}

func JobDisplayNames(candidates []JobCandidate) map[string]string {
	shortCounts := map[string]int{}
	for _, c := range candidates {
		shortCounts[JobKeyShort(c.Job.JobKey)]++
	}

	names := make(map[string]string, len(candidates))
	for _, c := range candidates {
		short := JobKeyShort(c.Job.JobKey)
		if shortCounts[short] > 1 {
			names[c.Job.JobKey] = c.Job.JobKey
		} else {
			names[c.Job.JobKey] = short
		}
	}
	return names
}

func WorkflowPathMatches(path, filter string) bool {
	if path == filter {
		return true
	}
	return strings.HasSuffix(path, "/"+filter)
}

func FormatJobList(candidates []JobCandidate) string {
	displayNames := JobDisplayNames(candidates)

	type workflowGroup struct {
		label string
		jobs  []JobCandidate
	}
	var groups []workflowGroup
	groupIdx := map[string]int{}
	for _, c := range candidates {
		label := c.WorkflowPath
		if label == "" {
			label = c.WorkflowName
		}
		if idx, ok := groupIdx[label]; ok {
			groups[idx].jobs = append(groups[idx].jobs, c)
		} else {
			groupIdx[label] = len(groups)
			groups = append(groups, workflowGroup{label: label, jobs: []JobCandidate{c}})
		}
	}

	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "\n  %s\n", g.label)
		for _, c := range g.jobs {
			fmt.Fprintf(&b, "    %s (%s)\n", displayNames[c.Job.JobKey], c.Job.Status)
		}
	}
	return b.String()
}

func MatchJobKey(jobKey, userKey string) int {
	if jobKey == userKey {
		return 1
	}
	if strings.HasSuffix(jobKey, ":"+userKey) {
		return 2
	}
	if strings.Contains(":"+jobKey+":", ":"+userKey+":") {
		return 3
	}
	return 0
}
