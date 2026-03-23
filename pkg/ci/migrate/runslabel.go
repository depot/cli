package migrate

import "strings"

// LabelClass categorizes a runs-on label.
type LabelClass int

const (
	LabelDepotNative    LabelClass = iota // depot-* prefixed labels
	LabelStandardGitHub                   // Standard GitHub-hosted runner labels
	LabelExpression                       // Dynamic expression labels containing ${{
	LabelNonstandard                      // Unknown/third-party labels
)

// GitHubToDepotRunner maps standard GitHub runner labels to their Depot equivalents.
var GitHubToDepotRunner = map[string]string{
	"ubuntu-latest": "depot-ubuntu-latest",
	"ubuntu-24.04":  "depot-ubuntu-24.04",
	"ubuntu-22.04":  "depot-ubuntu-22.04",
	"ubuntu-20.04":  "depot-ubuntu-20.04",
}

// ClassifyLabel determines the category of a runs-on label.
func ClassifyLabel(label string) LabelClass {
	lower := strings.ToLower(strings.TrimSpace(label))
	if lower == "" {
		return LabelNonstandard
	}

	if strings.Contains(label, "${{") {
		return LabelExpression
	}

	if strings.HasPrefix(lower, "depot-") {
		return LabelDepotNative
	}

	if _, ok := GitHubToDepotRunner[lower]; ok {
		return LabelStandardGitHub
	}

	return LabelNonstandard
}

// MapLabel maps a runs-on label to its Depot equivalent.
// Returns the new label, whether it changed, and a reason string for the comment.
func MapLabel(label string) (newLabel string, changed bool, reason string) {
	cls := ClassifyLabel(label)
	switch cls {
	case LabelDepotNative, LabelExpression:
		return label, false, ""
	case LabelStandardGitHub:
		lower := strings.ToLower(strings.TrimSpace(label))
		mapped := GitHubToDepotRunner[lower]
		return mapped, true, "Mapped standard GitHub runner to Depot equivalent."
	case LabelNonstandard:
		return "depot-ubuntu-latest", true, "Nonstandard GitHub runner label detected, changed to default Depot runner."
	default:
		return label, false, ""
	}
}
