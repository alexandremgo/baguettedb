package domain

import "testing"

func TestStatementsGrowStorage(t *testing.T) {
	tests := []struct {
		name  string
		stmts []ClassifiedStatement
		want  bool
	}{
		{
			name:  "insert grows",
			stmts: []ClassifiedStatement{{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbInsert}}},
			want:  true,
		},
		{
			name:  "update grows",
			stmts: []ClassifiedStatement{{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}}},
			want:  true,
		},
		{
			name:  "plain delete frees",
			stmts: []ClassifiedStatement{{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbDelete}}},
			want:  false,
		},
		{
			name: "delete with subquery frees",
			stmts: []ClassifiedStatement{{
				Type:     StatementDML,
				DMLVerbs: []StatementVerb{StatementVerbDelete, StatementVerbSelect},
			}},
			want: false,
		},
		{
			name: "mixed batch with one insert grows",
			stmts: []ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbDelete}},
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbInsert}},
			},
			want: true,
		},
		{
			name:  "DML without recorded verbs fails closed",
			stmts: []ClassifiedStatement{{Type: StatementDML}},
			want:  true,
		},
		{
			name:  "create table grows",
			stmts: []ClassifiedStatement{{Type: StatementDDL, DDLType: DDLCreateTable}},
			want:  true,
		},
		{
			name:  "alter table grows",
			stmts: []ClassifiedStatement{{Type: StatementDDL, DDLType: DDLAlterTable}},
			want:  true,
		},
		{
			name:  "create index grows",
			stmts: []ClassifiedStatement{{Type: StatementDDL, DDLType: DDLCreateIndex}},
			want:  true,
		},
		{
			name:  "unclassified DDL fails closed",
			stmts: []ClassifiedStatement{{Type: StatementDDL, DDLType: DDLUnknown}},
			want:  true,
		},
		{
			name: "all-drop DDL batch frees",
			stmts: []ClassifiedStatement{
				{Type: StatementDDL, DDLType: DDLDropTable},
				{Type: StatementDDL, DDLType: DDLDropIndex},
				{Type: StatementDDL, DDLType: DDLDropView},
				{Type: StatementDDL, DDLType: DDLDropTrigger},
			},
			want: false,
		},
		{
			name:  "DQL never grows",
			stmts: []ClassifiedStatement{{Type: StatementDQL, DMLVerbs: []StatementVerb{StatementVerbSelect}}},
			want:  false,
		},
		{
			name:  "empty batch",
			stmts: nil,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatementsGrowStorage(tt.stmts); got != tt.want {
				t.Errorf("StatementsGrowStorage() = %v, want %v", got, tt.want)
			}
		})
	}
}
