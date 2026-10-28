package api

// scopeDescriptions maps OAuth scope names to user-friendly descriptions
// shown on the consent page. Unknown scopes fall back to the raw scope name.
var scopeDescriptions = map[string]string{
	"openid":         "See your basic profile (name, email)",
	"offline_access": "Keep you signed in",
	"profile":        "Read your profile details",
	"email":          "Read your email address",
}

// scopeView holds both the raw scope name (posted back to accept/deny) and
// the friendly label rendered to the user.
type scopeView struct {
	Name  string
	Label string
}

// toScopeViews converts raw scope names into scopeView entries with friendly
// labels, falling back to the raw name when no description is registered.
func toScopeViews(rawScopes []string) []scopeView {
	views := make([]scopeView, len(rawScopes))
	for i, s := range rawScopes {
		label, ok := scopeDescriptions[s]
		if !ok {
			label = s
		}
		views[i] = scopeView{Name: s, Label: label}
	}
	return views
}
