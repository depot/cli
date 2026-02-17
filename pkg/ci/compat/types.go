package compat

// SupportLevel represents the level of support for a CI feature
type SupportLevel int

const (
	Supported SupportLevel = iota
	Unsupported
	InProgress
	Partial
)

// CompatibilityRule defines a rule for checking CI feature compatibility
type CompatibilityRule struct {
	Feature    string       `json:"feature"`
	Supported  SupportLevel `json:"supported"`
	Note       string       `json:"note"`
	Suggestion string       `json:"suggestion"`
}

// CompatibilityIssue represents a single compatibility issue found in a file
type CompatibilityIssue struct {
	File       string       `json:"file"`
	Feature    string       `json:"feature"`
	Level      SupportLevel `json:"level"`
	Message    string       `json:"message"`
	Suggestion string       `json:"suggestion"`
}

// CompatibilityReport contains all compatibility issues found in a file
type CompatibilityReport struct {
	File   string               `json:"file"`
	Issues []CompatibilityIssue `json:"issues"`
}
