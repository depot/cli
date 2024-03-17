package actionspublic

// GitHub types

type EventPayload struct {
	Repository  *Repository  `json:"repository"`
	PullRequest *PullRequest `json:"pull_request"`
}

type Repository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

type PullRequest struct {
	Head *Head `json:"head"`
}

type Head struct {
	Repo *Repository `json:"repo"`
}

// OIDC issuer types

type ClaimRequest struct {
	Aud       string `json:"aud"`
	EventName string `json:"eventName"`
	Repo      string `json:"repo"`
	RunID     string `json:"runID"`
}

type ChallengeResponse struct {
	ChallengeCode string `json:"challengeCode"`
	ExchangeURL   string `json:"exchangeURL"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
