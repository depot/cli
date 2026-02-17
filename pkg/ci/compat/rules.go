package compat

// TriggerRules defines support levels for GitHub Actions trigger events.
var TriggerRules = map[string]CompatibilityRule{
	"push": {
		Feature:   "push",
		Supported: Supported,
		Note:      "Push events are supported.",
	},
	"pull_request": {
		Feature:   "pull_request",
		Supported: Supported,
		Note:      "Pull request events are supported.",
	},
	"pull_request_target": {
		Feature:   "pull_request_target",
		Supported: Supported,
		Note:      "Pull request target events are supported.",
	},
	"schedule": {
		Feature:   "schedule",
		Supported: Supported,
		Note:      "Scheduled workflows are supported.",
	},
	"workflow_call": {
		Feature:   "workflow_call",
		Supported: Supported,
		Note:      "Reusable workflow entrypoints are supported.",
	},
	"workflow_dispatch": {
		Feature:   "workflow_dispatch",
		Supported: Supported,
		Note:      "Manual dispatch is supported.",
	},
	"workflow_run": {
		Feature:   "workflow_run",
		Supported: Supported,
		Note:      "Workflow run triggers are supported.",
	},
	"branch_protection_rule": {
		Feature:    "branch_protection_rule",
		Supported:  Unsupported,
		Note:       "Branch protection rule events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"check_run": {
		Feature:    "check_run",
		Supported:  Unsupported,
		Note:       "Check run events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"check_suite": {
		Feature:    "check_suite",
		Supported:  Unsupported,
		Note:       "Check suite events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"create": {
		Feature:    "create",
		Supported:  Unsupported,
		Note:       "Create events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"delete": {
		Feature:    "delete",
		Supported:  Unsupported,
		Note:       "Delete events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"deployment": {
		Feature:    "deployment",
		Supported:  Unsupported,
		Note:       "Deployment events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"deployment_status": {
		Feature:    "deployment_status",
		Supported:  Unsupported,
		Note:       "Deployment status events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"discussion": {
		Feature:    "discussion",
		Supported:  Unsupported,
		Note:       "Discussion events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"discussion_comment": {
		Feature:    "discussion_comment",
		Supported:  Unsupported,
		Note:       "Discussion comment events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"fork": {
		Feature:    "fork",
		Supported:  Unsupported,
		Note:       "Fork events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"gollum": {
		Feature:    "gollum",
		Supported:  Unsupported,
		Note:       "Wiki events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"issue_comment": {
		Feature:    "issue_comment",
		Supported:  Unsupported,
		Note:       "Issue comment events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"issues": {
		Feature:    "issues",
		Supported:  Unsupported,
		Note:       "Issue events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"label": {
		Feature:    "label",
		Supported:  Unsupported,
		Note:       "Label events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"merge_group": {
		Feature:    "merge_group",
		Supported:  Unsupported,
		Note:       "Merge group events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"milestone": {
		Feature:    "milestone",
		Supported:  Unsupported,
		Note:       "Milestone events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"page_build": {
		Feature:    "page_build",
		Supported:  Unsupported,
		Note:       "Page build events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"project": {
		Feature:    "project",
		Supported:  Unsupported,
		Note:       "Project events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"project_card": {
		Feature:    "project_card",
		Supported:  Unsupported,
		Note:       "Project card events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"project_column": {
		Feature:    "project_column",
		Supported:  Unsupported,
		Note:       "Project column events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"public": {
		Feature:    "public",
		Supported:  Unsupported,
		Note:       "Public events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"registry_package": {
		Feature:    "registry_package",
		Supported:  Unsupported,
		Note:       "Registry package events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"release": {
		Feature:    "release",
		Supported:  Unsupported,
		Note:       "Release events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"repository_dispatch": {
		Feature:    "repository_dispatch",
		Supported:  Unsupported,
		Note:       "Repository dispatch events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"status": {
		Feature:    "status",
		Supported:  Unsupported,
		Note:       "Status events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
	"watch": {
		Feature:    "watch",
		Supported:  Unsupported,
		Note:       "Watch events are not supported.",
		Suggestion: "Remove this trigger or use a webhook-based alternative.",
	},
}

// JobFeatureRules defines support levels for job-level syntax and keys.
var JobFeatureRules = map[string]CompatibilityRule{
	"needs": {
		Feature:   "needs",
		Supported: Supported,
		Note:      "Job dependencies are supported.",
	},
	"if": {
		Feature:   "if",
		Supported: Supported,
		Note:      "Job conditions are supported.",
	},
	"env": {
		Feature:   "env",
		Supported: Supported,
		Note:      "Job environment variables are supported.",
	},
	"defaults": {
		Feature:   "defaults",
		Supported: Supported,
		Note:      "Job defaults are supported.",
	},
	"timeout-minutes": {
		Feature:   "timeout-minutes",
		Supported: Supported,
		Note:      "Job timeouts are supported.",
	},
	"continue-on-error": {
		Feature:   "continue-on-error",
		Supported: Supported,
		Note:      "Continue-on-error is supported.",
	},
	"strategy.matrix": {
		Feature:   "strategy.matrix",
		Supported: Supported,
		Note:      "Matrix strategies are supported.",
	},
	"strategy.fail-fast": {
		Feature:   "strategy.fail-fast",
		Supported: Supported,
		Note:      "Matrix fail-fast is supported.",
	},
	"strategy.max-parallel": {
		Feature:   "strategy.max-parallel",
		Supported: Supported,
		Note:      "Matrix max-parallel is supported.",
	},
	"outputs": {
		Feature:   "outputs",
		Supported: Supported,
		Note:      "Job outputs are supported.",
	},
	"uses": {
		Feature:   "uses",
		Supported: Partial,
		Note:      "Reusable workflows are only supported from the same repository.",
	},
	"runs-on (custom labels)": {
		Feature:    "runs-on (custom labels)",
		Supported:  Unsupported,
		Note:       "Non-Depot runner labels are not supported.",
		Suggestion: "Use Depot-supported labels, such as depot_ubuntu_latest.",
	},
	"environment": {
		Feature:    "environment",
		Supported:  Unsupported,
		Note:       "Deployment environments are not supported.",
		Suggestion: "Move environment-specific logic into variables and conditional steps.",
	},
	"concurrency": {
		Feature:    "concurrency",
		Supported:  InProgress,
		Note:       "Concurrency support is in progress.",
		Suggestion: "Avoid relying on concurrency groups until support is complete.",
	},
	"permissions": {
		Feature:    "permissions",
		Supported:  Partial,
		Note:       "Permissions are supported except OIDC-related id-token scopes.",
		Suggestion: "Avoid id-token and OIDC-dependent workflows for now.",
	},
	"services": {
		Feature:    "services",
		Supported:  Unsupported,
		Note:       "Service containers are not supported in this compatibility profile.",
		Suggestion: "Move service dependencies to external managed services or pre-provisioned resources.",
	},
	"container": {
		Feature:    "container",
		Supported:  Unsupported,
		Note:       "Job-level containers are not supported in this compatibility profile.",
		Suggestion: "Run directly on supported runners or use setup actions instead of job containers.",
	},
}

// StepFeatureRules defines support levels for step-level syntax and keys.
var StepFeatureRules = map[string]CompatibilityRule{
	"id": {
		Feature:   "id",
		Supported: Supported,
		Note:      "Step identifiers are supported.",
	},
	"name": {
		Feature:   "name",
		Supported: Supported,
		Note:      "Step names are supported.",
	},
	"if": {
		Feature:   "if",
		Supported: Supported,
		Note:      "Step conditions are supported.",
	},
	"uses": {
		Feature:   "uses",
		Supported: Supported,
		Note:      "Action references are supported.",
	},
	"run": {
		Feature:   "run",
		Supported: Supported,
		Note:      "Shell command steps are supported.",
	},
	"shell": {
		Feature:   "shell",
		Supported: Supported,
		Note:      "Shell selection is supported.",
	},
	"with": {
		Feature:   "with",
		Supported: Supported,
		Note:      "Action inputs are supported.",
	},
	"env": {
		Feature:   "env",
		Supported: Supported,
		Note:      "Step environment variables are supported.",
	},
	"working-directory": {
		Feature:   "working-directory",
		Supported: Supported,
		Note:      "Step working directories are supported.",
	},
	"continue-on-error": {
		Feature:   "continue-on-error",
		Supported: Supported,
		Note:      "Step continue-on-error is supported.",
	},
	"timeout-minutes": {
		Feature:   "timeout-minutes",
		Supported: Supported,
		Note:      "Step timeouts are supported.",
	},
}

// ExpressionRules defines support levels for expression contexts and functions.
var ExpressionRules = map[string]CompatibilityRule{
	"github": {
		Feature:   "github",
		Supported: Supported,
		Note:      "The github expression context is supported.",
	},
	"env": {
		Feature:   "env",
		Supported: Supported,
		Note:      "The env expression context is supported.",
	},
	"vars": {
		Feature:   "vars",
		Supported: Supported,
		Note:      "The vars expression context is supported.",
	},
	"secrets": {
		Feature:   "secrets",
		Supported: Supported,
		Note:      "The secrets expression context is supported.",
	},
	"needs": {
		Feature:   "needs",
		Supported: Supported,
		Note:      "The needs expression context is supported.",
	},
	"strategy": {
		Feature:   "strategy",
		Supported: Supported,
		Note:      "The strategy expression context is supported.",
	},
	"matrix": {
		Feature:   "matrix",
		Supported: Supported,
		Note:      "The matrix expression context is supported.",
	},
	"steps": {
		Feature:   "steps",
		Supported: Supported,
		Note:      "The steps expression context is supported.",
	},
	"job": {
		Feature:   "job",
		Supported: Supported,
		Note:      "The job expression context is supported.",
	},
	"runner": {
		Feature:   "runner",
		Supported: Supported,
		Note:      "The runner expression context is supported.",
	},
	"inputs": {
		Feature:   "inputs",
		Supported: Supported,
		Note:      "The inputs expression context is supported.",
	},
	"always()": {
		Feature:   "always()",
		Supported: Supported,
		Note:      "The always() function is supported.",
	},
	"success()": {
		Feature:   "success()",
		Supported: Supported,
		Note:      "The success() function is supported.",
	},
	"failure()": {
		Feature:   "failure()",
		Supported: Supported,
		Note:      "The failure() function is supported.",
	},
	"cancelled()": {
		Feature:   "cancelled()",
		Supported: Supported,
		Note:      "The cancelled() function is supported.",
	},
	"hashFiles()": {
		Feature:    "hashFiles()",
		Supported:  InProgress,
		Note:       "The hashFiles() function is in progress.",
		Suggestion: "Use deterministic cache keys without hashFiles() until support is complete.",
	},
	"contains()": {
		Feature:   "contains()",
		Supported: Supported,
		Note:      "The contains() function is supported.",
	},
	"startsWith()": {
		Feature:   "startsWith()",
		Supported: Supported,
		Note:      "The startsWith() function is supported.",
	},
	"endsWith()": {
		Feature:   "endsWith()",
		Supported: Supported,
		Note:      "The endsWith() function is supported.",
	},
	"format()": {
		Feature:   "format()",
		Supported: Supported,
		Note:      "The format() function is supported.",
	},
	"join()": {
		Feature:   "join()",
		Supported: Supported,
		Note:      "The join() function is supported.",
	},
	"toJSON()": {
		Feature:   "toJSON()",
		Supported: Supported,
		Note:      "The toJSON() function is supported.",
	},
	"fromJSON()": {
		Feature:   "fromJSON()",
		Supported: Supported,
		Note:      "The fromJSON() function is supported.",
	},
}
