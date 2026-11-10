//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// fakeFormulaKeys answers keys as a bdb's Data API keys by formula ID, or fails with err.
type fakeFormulaKeys struct {
	keys map[string][]apptypes.DataAPIKey
	err  error
}

// FormulaKeys answers the fake's keys, or its error.
func (f *fakeFormulaKeys) FormulaKeys(_ context.Context, _, _ uuid.UUID) (map[string][]apptypes.DataAPIKey, error) {
	return f.keys, f.err
}

// TestMCP_DescribeBdb_FormulaDataAPIKeys checks that describe_bdb names the Data API keys apps run each formula with,
// by ID and name alone, and none on a formula without keys, and that a failed key lookup still describes the bdb with
// its formulas, and no keys.
func TestMCP_DescribeBdb_FormulaDataAPIKeys(t *testing.T) {
	truncateAll(t)
	userID, keyID := uuid.New(), uuid.New()
	onKeys := uuid.NewString()
	dbApp := &mockDatabasesApp{formulas: []apptypes.Formula{
		{ID: onKeys, Name: "all-notes", Kind: "view"},
		{ID: uuid.NewString(), Name: "note-count", Kind: "view"},
	}}

	for name, tt := range map[string]struct {
		keys *fakeFormulaKeys
		want map[string][]string
	}{
		"keys found": {
			keys: &fakeFormulaKeys{keys: map[string][]apptypes.DataAPIKey{
				onKeys: {{ID: keyID, Name: "phone app", TokenPrefix: "bdbdk_abcd"}},
			}},
			want: map[string][]string{"all-notes": {"phone app"}},
		},
		"lookup failed": {keys: &fakeFormulaKeys{err: errors.New("auth unreachable")}},
	} {
		t.Run(name, func(t *testing.T) {
			c, bdbSvc := setupMCPClientWithKeys(t, userID.String(), dbApp, tt.keys)
			bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Notes "+name, "")
			if err != nil {
				t.Fatalf("creating a bdb: %v", err)
			}

			overview := parseToolJSON(t, callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()}))
			formulas, _ := overview["formulas"].([]any)
			if len(formulas) != 2 {
				t.Fatalf("describe_bdb's formulas = %v, want both", overview["formulas"])
			}
			for _, raw := range formulas {
				f, _ := raw.(map[string]any)
				formula, _ := f["name"].(string)
				keys, _ := f["data_api_keys"].([]any)
				var names []string
				for _, k := range keys {
					key, _ := k.(map[string]any)
					if len(key) != 2 || key["id"] != keyID.String() {
						t.Errorf("key %v of %s, want its ID and name alone", key, formula)
					}
					keyName, _ := key["name"].(string)
					names = append(names, keyName)
				}
				if !slices.Equal(names, tt.want[formula]) {
					t.Errorf("keys of %s = %v, want %v", formula, names, tt.want[formula])
				}
			}
		})
	}
}
