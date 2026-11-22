package domain

import "slices"

// BindNamedParameters builds one positional argument slice per statement from values keyed by placeholder name,
// without its sigil ("x" for ":x"). Each statement binds the names it uses, so a name may bind in several statements.
// It returns nil when no statement binds anything.
//
// A bare "?" placeholder returns ErrUnnamedParameter, placeholders without a value a *MissingParametersError naming
// them, and values that no statement uses an *UnusedParametersError.
func BindNamedParameters(stmts []ClassifiedStatement, values map[string]TypedValue) ([][]TypedValue, error) {
	argsPerStmt := make([][]TypedValue, len(stmts))
	used := make(map[string]bool, len(values))
	var missing []string
	bound := false

	for i, s := range stmts {
		args := make([]TypedValue, 0, len(s.ParamNames))
		for _, name := range s.ParamNames {
			if name == "" {
				return nil, ErrUnnamedParameter
			}
			value, ok := values[name]
			if !ok {
				if !slices.Contains(missing, name) {
					missing = append(missing, name)
				}
				continue
			}
			used[name] = true
			args = append(args, value)
		}
		argsPerStmt[i] = args
		bound = bound || len(args) > 0
	}

	if len(missing) > 0 {
		return nil, &MissingParametersError{Names: missing}
	}
	if unused := unusedParameterNames(values, used); len(unused) > 0 {
		return nil, &UnusedParametersError{Names: unused}
	}
	if !bound {
		return nil, nil
	}
	return argsPerStmt, nil
}

// unusedParameterNames returns the names of values that are not in used, sorted.
func unusedParameterNames(values map[string]TypedValue, used map[string]bool) []string {
	var unused []string
	for name := range values {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	slices.Sort(unused)
	return unused
}
