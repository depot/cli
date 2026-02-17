package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type ghWorkflow struct {
	Name string           `yaml:"name"`
	On   interface{}      `yaml:"on"`
	Jobs map[string]ghJob `yaml:"jobs"`
}

type ghJob struct {
	RunsOn    interface{}            `yaml:"runs-on"`
	Needs     interface{}            `yaml:"needs"`
	If        string                 `yaml:"if"`
	Container interface{}            `yaml:"container"`
	Services  map[string]interface{} `yaml:"services"`
	Strategy  *ghStrategy            `yaml:"strategy"`
	Uses      string                 `yaml:"uses"`
	Steps     []ghStep               `yaml:"steps"`
}

type ghStrategy struct {
	Matrix interface{} `yaml:"matrix"`
}

type ghStep struct {
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

// ParseWorkflowFile parses a single GitHub Actions YAML file.
func ParseWorkflowFile(path string) (*WorkflowFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file %s: %w", path, err)
	}

	if len(strings.TrimSpace(string(content))) == 0 {
		return nil, errors.New("workflow file is empty")
	}

	var wf ghWorkflow
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return nil, fmt.Errorf("failed to parse workflow YAML %s: %w", path, err)
	}

	triggers := extractTriggers(wf.On)
	jobs := extractJobs(wf.Jobs)

	name := wf.Name
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	return &WorkflowFile{
		Path:     path,
		Name:     name,
		Triggers: triggers,
		Jobs:     jobs,
	}, nil
}

// ParseWorkflowDir parses all .yml/.yaml files in a directory.
func ParseWorkflowDir(dir string) ([]*WorkflowFile, error) {
	var workflows []*WorkflowFile

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		wf, err := ParseWorkflowFile(path)
		if err != nil {
			return err
		}

		workflows = append(workflows, wf)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return workflows, nil
}

func extractTriggers(on interface{}) []string {
	switch v := on.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		triggers := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				triggers = append(triggers, s)
			}
		}
		return triggers
	case map[string]interface{}:
		triggers := make([]string, 0, len(v))
		for trigger := range v {
			if trigger != "" {
				triggers = append(triggers, trigger)
			}
		}
		sort.Strings(triggers)
		return triggers
	case map[interface{}]interface{}:
		triggers := make([]string, 0, len(v))
		for key := range v {
			s, ok := key.(string)
			if ok && s != "" {
				triggers = append(triggers, s)
			}
		}
		sort.Strings(triggers)
		return triggers
	default:
		return nil
	}
}

func extractJobs(jobsMap map[string]ghJob) []JobInfo {
	if len(jobsMap) == 0 {
		return nil
	}

	names := make([]string, 0, len(jobsMap))
	for name := range jobsMap {
		names = append(names, name)
	}
	sort.Strings(names)

	jobs := make([]JobInfo, 0, len(names))
	for _, name := range names {
		job := jobsMap[name]
		jobs = append(jobs, JobInfo{
			Name:         name,
			RunsOn:       stringifyRunsOn(job.RunsOn),
			UsesReusable: job.Uses,
			HasMatrix:    job.Strategy != nil && job.Strategy.Matrix != nil,
			HasContainer: job.Container != nil,
			HasServices:  len(job.Services) > 0,
		})
	}

	return jobs
}

func stringifyRunsOn(runsOn interface{}) string {
	switch v := runsOn.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, ",")
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}
