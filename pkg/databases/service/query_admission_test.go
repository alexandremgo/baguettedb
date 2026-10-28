package service

import (
	"errors"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestValidateDDLAdmission verifies the per-statement DDL admission checks: created-object names must pass
// the identifier rule (statements creating nothing are exempt), and WITHOUT ROWID stays rejected.
func TestValidateDDLAdmission(t *testing.T) {
	tests := []struct {
		name    string
		stmt    domain.ClassifiedStatement
		wantErr error
	}{
		{
			"valid create table",
			domain.ClassifiedStatement{SQL: "CREATE TABLE notes (id INTEGER)", CreatedObjects: []string{"notes"}},
			nil,
		},
		{
			"keyword name allowed",
			domain.ClassifiedStatement{SQL: `CREATE TABLE "order" (id INTEGER)`, CreatedObjects: []string{"order"}},
			nil,
		},
		{
			"no created objects (DROP)",
			domain.ClassifiedStatement{SQL: "DROP TABLE notes"},
			nil,
		},
		{
			"name with space rejected",
			domain.ClassifiedStatement{
				SQL:            `CREATE TABLE "my table" (id INTEGER)`,
				CreatedObjects: []string{"my table"},
			},
			domain.ErrUnsupportedStatement,
		},
		{
			"accented name rejected",
			domain.ClassifiedStatement{SQL: `CREATE TABLE café (id INTEGER)`, CreatedObjects: []string{"café"}},
			domain.ErrUnsupportedStatement,
		},
		{
			"second created object rejected",
			domain.ClassifiedStatement{
				SQL:            "CREATE TABLE ok (id INTEGER)",
				CreatedObjects: []string{"ok", "not ok"},
			},
			domain.ErrUnsupportedStatement,
		},
		{
			"without rowid still rejected",
			domain.ClassifiedStatement{
				SQL:            "CREATE TABLE kv (k TEXT PRIMARY KEY) WITHOUT ROWID",
				CreatedObjects: []string{"kv"},
			},
			domain.ErrWithoutRowIDNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDDLAdmission(tt.stmt)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("validateDDLAdmission() returned error: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("validateDDLAdmission() = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}
