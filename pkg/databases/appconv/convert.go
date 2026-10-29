// Package appconv converts the databases app's domain types into the cross-app transfer shapes of
// pkg/common/apptypes. It is behavior-free field mapping shared by every exit point where domain values leave the
// app — its api and mcp layers and the in-process adapters of consuming apps — so a new field is mapped once.
package appconv

import (
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TableInfos maps table listing entries.
func TableInfos(infos []domain.TableInfo) []apptypes.TableInfo {
	out := make([]apptypes.TableInfo, len(infos))
	for i, info := range infos {
		out[i] = apptypes.TableInfo{Name: info.Name, Type: info.Type, Module: info.Module}
	}
	return out
}

// TableDescription maps a table's schema. Columns and foreign keys are always non-nil slices, so they encode as
// [] rather than null.
func TableDescription(td *domain.TableDescription) apptypes.TableDescription {
	cols := make([]apptypes.TableColumn, len(td.Columns))
	for i, c := range td.Columns {
		cols[i] = apptypes.TableColumn{
			Name: c.Name, Type: c.Type, NotNull: c.NotNull, Default: c.Default, PrimaryKey: c.PrimaryKey,
		}
	}
	fks := make([]apptypes.ForeignKey, len(td.ForeignKeys))
	for i, fk := range td.ForeignKeys {
		fks[i] = apptypes.ForeignKey{
			From: fk.From, Table: fk.Table, To: fk.To, OnUpdate: fk.OnUpdate, OnDelete: fk.OnDelete,
		}
	}
	return apptypes.TableDescription{
		Columns: cols, ForeignKeys: fks, Kind: td.Kind, Module: td.Module, Checks: td.Checks,
	}
}

// TableSchemas maps tables with their full description.
func TableSchemas(schemas []domain.TableSchema) []apptypes.TableSchema {
	out := make([]apptypes.TableSchema, len(schemas))
	for i, s := range schemas {
		out[i] = apptypes.TableSchema{
			Name:          s.Name,
			Type:          s.Type,
			Module:        s.Module,
			Schema:        TableDescription(&s.Schema),
			DescribeError: s.DescribeError,
		}
	}
	return out
}

// Skills maps skills with their bodies, each as Skill does.
func Skills(skills []*domain.Skill) []apptypes.Skill {
	out := make([]apptypes.Skill, len(skills))
	for i, s := range skills {
		out[i] = Skill(s)
	}
	return out
}

// Skill maps one skill with its body. Target tables are copied into a non-nil slice, so a skill without targets
// encodes as [] rather than null.
func Skill(s *domain.Skill) apptypes.Skill {
	return apptypes.Skill{
		ID:           s.ID.String(),
		Name:         s.Name,
		Description:  s.Description,
		Body:         s.Body,
		TargetTables: append([]string{}, s.TargetTables...),
	}
}

// Formulas maps formulas to their lightweight orientation view.
func Formulas(formulas []*domain.Formula) []apptypes.Formula {
	out := make([]apptypes.Formula, len(formulas))
	for i, f := range formulas {
		out[i] = apptypes.Formula{
			ID:          f.ID.String(),
			Name:        f.Name,
			Label:       f.Label,
			Description: f.Description,
			Kind:        string(f.Kind),
			Hidden:      f.Hidden,
		}
	}
	return out
}
