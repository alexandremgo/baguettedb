package api

import "github.com/alexandremgo/baguettedb/pkg/starters/domain"

// errorResponse is the standard error payload.
type errorResponse struct {
	Error string `json:"error"`
}

// starterFormulaPreview is one formula on a catalog card: enough to convey what the starter can do, without the
// SQL or parameter schema. Attached and Hidden let the card distinguish the starter's visible views from the
// actions and plumbing riding along.
type starterFormulaPreview struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
	Attached    bool   `json:"attached,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
}

// starterSkillPreview is one skill on a catalog card.
type starterSkillPreview struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// starterResponse is one catalog entry of GET /starters. The preview carries what the manifest itself knows:
// formula and skill names + descriptions (kinds and table names only exist once a starter is applied).
type starterResponse struct {
	Key         string                  `json:"key"`
	Version     int                     `json:"version"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Icon        string                  `json:"icon,omitempty"`
	Tags        []string                `json:"tags,omitempty"`
	Formulas    []starterFormulaPreview `json:"formulas"`
	Skills      []starterSkillPreview   `json:"skills"`
}

// createFromStarterRequest is the JSON body for POST /bdbs/from-starter. Name and Description override the
// manifest defaults when non-empty.
type createFromStarterRequest struct {
	StarterKey  string `json:"starter_key"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// toStarterResponse converts a manifest into its catalog entry.
func toStarterResponse(m domain.StarterManifest) starterResponse {
	formulas := make([]starterFormulaPreview, len(m.Formulas))
	for i, f := range m.Formulas {
		formulas[i] = starterFormulaPreview{
			Name: f.Name, Label: f.Label, Description: f.Description,
			Attached: f.AttachedTo != "", Hidden: f.Hidden,
		}
	}
	skills := make([]starterSkillPreview, len(m.Skills))
	for i, s := range m.Skills {
		skills[i] = starterSkillPreview{Name: s.Name, Description: s.Description}
	}
	return starterResponse{
		Key:         m.Key,
		Version:     m.Version,
		Name:        m.Name,
		Description: m.Description,
		Icon:        m.Icon,
		Tags:        m.Tags,
		Formulas:    formulas,
		Skills:      skills,
	}
}
