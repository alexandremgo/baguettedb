package domain

import "fmt"

// ParamValueError reports a value a formula run was given for one of its parameters, or the lack of one, that the
// parameter refuses: the formula is fine, the caller's input is not. The message names no surface, so each one can
// word the parameter its own way (a JSON key, a --param flag).
type ParamValueError struct {
	// Param is the parameter's name.
	Param string

	// Problem says what is wrong with the value, as a predicate of the parameter, e.g. `expects an integer, got
	// "four"` or "needs a value".
	Problem string
}

// Error names the parameter, then the problem, e.g. `parameter "rating" expects an integer, got "four"`.
func (e *ParamValueError) Error() string {
	return fmt.Sprintf("parameter %q %s", e.Param, e.Problem)
}

// Unwrap returns ErrFormulaParamValue, so errors.Is matches the sentinel.
func (e *ParamValueError) Unwrap() error {
	return ErrFormulaParamValue
}
