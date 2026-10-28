package domain

// NewerPlatformSchemaError reports ErrNewerPlatformSchema in Message, words that tell the user how to run the newer
// version wherever they meet the refusal.
type NewerPlatformSchemaError struct {
	Message string
}

// Error returns the message.
func (e *NewerPlatformSchemaError) Error() string { return e.Message }

// Unwrap ties the typed error to the ErrNewerPlatformSchema sentinel.
func (e *NewerPlatformSchemaError) Unwrap() error { return ErrNewerPlatformSchema }
