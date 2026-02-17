package api

// CISecretAddRequest is the request payload for adding a CI secret
type CISecretAddRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CISecretBatchAddRequest is the request payload for adding multiple CI secrets
type CISecretBatchAddRequest struct {
	Secrets []CISecretAddRequest `json:"secrets"`
}

// CISecretMeta contains metadata about a CI secret
type CISecretMeta struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// CISecretListResponse is the response from listing CI secrets
type CISecretListResponse struct {
	Secrets []CISecretMeta `json:"secrets"`
}

// CIVariableAddRequest is the request payload for adding a CI variable
type CIVariableAddRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CIVariableMeta contains metadata about a CI variable
type CIVariableMeta struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// CIVariableListResponse is the response from listing CI variables
type CIVariableListResponse struct {
	Variables []CIVariableMeta `json:"variables"`
}
