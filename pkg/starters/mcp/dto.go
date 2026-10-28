package mcp

import "github.com/alexandremgo/baguettedb/pkg/starters/domain"

// starterFormula is one formula on a catalog entry. Name is the stable reference the formula keeps once the
// bdb exists; Label is the optional human name the app renders instead, absent when the manifest sets none.
type starterFormula struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
}

// starterEntry is one starter in the list_starters response. It carries what choosing a starter takes — the key
// to create from, and the surface the new bdb opens with — and omits the manifest's schema, SQL, and icon.
type starterEntry struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Tags        []string         `json:"tags,omitempty"`
	Formulas    []starterFormula `json:"formulas"`
	Skills      []string         `json:"skills"`
}

// visibleFormulas returns the manifest's top-level, non-hidden formulas — the ones a member sees listed in
// the app — each under both its name and its label, so the entries join against what the seeded bdb reports.
func visibleFormulas(m domain.StarterManifest) []starterFormula {
	formulas := []starterFormula{}

	for _, f := range m.Formulas {
		if !f.Hidden && f.AttachedTo == "" {
			formulas = append(formulas, starterFormula{
				Name:        f.Name,
				Label:       f.Label,
				Description: f.Description,
			})
		}
	}

	return formulas
}

// toStarterEntry converts a manifest into its catalog entry.
func toStarterEntry(m domain.StarterManifest) starterEntry {
	skills := make([]string, len(m.Skills))
	for i, s := range m.Skills {
		skills[i] = s.Name
	}

	return starterEntry{
		Key:         m.Key,
		Name:        m.Name,
		Description: m.Description,
		Tags:        m.Tags,
		Formulas:    visibleFormulas(m),
		Skills:      skills,
	}
}

// createdBdb is the create_bdb response payload. StarterKey is empty for a blank bdb.
type createdBdb struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StarterKey  string `json:"starter_key,omitempty"`
	NextStep    string `json:"next_step"`
}
