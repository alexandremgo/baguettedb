package domain

import (
	"fmt"
	"time"
)

// QueryRateLimitedError reports the caller's SQL concurrency cap being reached and how long to wait before retrying.
// Unwraps to ErrTooManyConcurrentQueries.
type QueryRateLimitedError struct {
	RetryAfter time.Duration
}

// Error returns the limit message with the rounded wait.
func (e *QueryRateLimitedError) Error() string {
	return fmt.Sprintf("%v; retry in %s", ErrTooManyConcurrentQueries, e.RetryAfter.Round(time.Second))
}

// Unwrap ties the typed error to the ErrTooManyConcurrentQueries sentinel.
func (e *QueryRateLimitedError) Unwrap() error { return ErrTooManyConcurrentQueries }
