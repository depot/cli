package migrate

// WorkflowFile represents a parsed CI workflow file
type WorkflowFile struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Triggers  []string  `json:"triggers"`
	Jobs      []JobInfo `json:"jobs"`
	Secrets   []string  `json:"secrets"`
	Variables []string  `json:"variables"`
}

// JobInfo contains information about a job in a workflow
type JobInfo struct {
	Name         string `json:"name"`
	RunsOn       string `json:"runs_on"`
	UsesReusable string `json:"uses_reusable,omitempty"`
	HasMatrix    bool   `json:"has_matrix"`
	HasContainer bool   `json:"has_container"`
	HasServices  bool   `json:"has_services"`
}

// MigrationPlan contains the plan for migrating workflows
type MigrationPlan struct {
	Workflows         []*WorkflowFile `json:"workflows"`
	DetectedSecrets   []string        `json:"detected_secrets"`
	DetectedVariables []string        `json:"detected_variables"`
}

// MigrationResult contains the results of a migration
type MigrationResult struct {
	FilesCopied       []string `json:"files_copied"`
	SecretsConfigured []string `json:"secrets_configured"`
	Warnings          []string `json:"warnings"`
}
