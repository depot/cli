package api

// DepotError wraps the error interface
type DepotError struct {
	Err error
}

// Error returns the error message
func (e *DepotError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying error
func (e *DepotError) Unwrap() error {
	return e.Err
}

// NewDepotError returns a new DepotError
func NewDepotError(err error) *DepotError {
	return &DepotError{Err: err}
}

// IsDepotError returns true if the error is a DepotError
func IsDepotError(err error) bool {
	_, ok := err.(*DepotError)
	return ok
}
