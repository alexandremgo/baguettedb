package domain

import (
	"errors"
	"slices"
	"testing"
)

func TestValidateFormulaDisplay(t *testing.T) {
	cols := []string{"rowid", "item", "notes", "bought"}
	window := ParamSchema{
		{Name: ReservedParamWindowStart, Type: ParamTypeDate, Source: ParamSourceWindow},
		{Name: ReservedParamWindowEnd, Type: ParamTypeDate, Source: ParamSourceWindow},
	}

	tests := []struct {
		name     string
		display  FormulaDisplay
		cols     []string
		params   ParamSchema
		attached bool
		wantErr  bool
	}{
		{
			name: "valid checklist on an attached detail view",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Add: "log-item", Edit: "fix-item", Delete: "drop-item"},
			},
			cols:     cols,
			attached: true,
		},
		{
			name: "format-only table mode on an attached detail view",
			display: FormulaDisplay{
				Mode:    DisplayModeTable,
				Columns: []FormulaDisplayColumn{{Column: "item", Format: DisplayFormatMarkdown}},
			},
			cols:     cols,
			attached: true,
		},
		{
			name: "slotted table mode on an attached detail view",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Delete: "drop-item"},
			},
			cols:     cols,
			attached: true,
			wantErr:  true,
		},
		{
			name: "markdown format in any mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody, Format: DisplayFormatMarkdown},
				},
			},
			cols: cols,
		},
		{
			name: "unknown column format",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle, Format: "html"},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "duplicate role",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleTitle},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "entry with neither role nor format",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes"},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "hidden columns in table mode",
			display: FormulaDisplay{
				Mode: DisplayModeTable,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Hidden: true},
					{Column: "notes", Hidden: true},
				},
			},
			cols: cols,
		},
		{
			name: "hidden column also carrying a role",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody, Hidden: true},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "detail slot on an attached detail view nests one level further",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Detail: "nested-detail"},
			},
			cols:     cols,
			attached: true,
		},
		{
			name: "valid checklist with check column and slots",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleCheck},
				},
				Slots: FormulaDisplaySlots{Add: "add-item", Toggle: "toggle-bought", Delete: "delete-item"},
			},
			cols: cols,
		},
		{
			name: "valid cards with body column",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody},
				},
			},
			cols: cols,
		},
		{
			name: "valid table with bulk list",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Bulk: []string{"archive-selected", "delete-selected"}},
			},
			cols: cols,
		},
		{
			name: "bulk list on a checklist",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Bulk: []string{"archive-selected"}},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "bulk list on an attached detail view",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Bulk: []string{"archive-selected"}},
			},
			cols:     cols,
			attached: true,
			wantErr:  true,
		},
		{
			name: "table bulk list without rowid column",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Bulk: []string{"archive-selected"}},
			},
			cols:    []string{"item"},
			wantErr: true,
		},
		{
			name: "valid checklist with row_actions list",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{RowActions: []string{"duplicate-item"}},
			},
			cols: cols,
		},
		{
			name: "valid row_actions on an attached detail view",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{RowActions: []string{"flag-item"}},
			},
			cols:     cols,
			attached: true,
		},
		{
			name: "valid row_actions list on a table",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{RowActions: []string{"duplicate-item"}},
			},
			cols: cols,
		},
		{
			name: "valid checklist add slot without rowid column",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Add: "add-item"},
			},
			cols: []string{"item"},
		},
		{
			name: "valid checklist check column without toggle slot",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleCheck},
				},
			},
			cols: cols,
		},
		{
			name:    "valid bare table config",
			display: FormulaDisplay{Mode: DisplayModeTable},
			cols:    cols,
		},
		{
			name: "valid table with detail slot",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Detail: "item-detail"},
			},
			cols: cols,
		},
		{
			name: "valid checklist with detail slot",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Detail: "item-detail"},
			},
			cols: cols,
		},
		{
			name: "table with title column",
			display: FormulaDisplay{
				Mode:    DisplayModeTable,
				Columns: []FormulaDisplayColumn{{Column: "item", Role: DisplayRoleTitle}},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "valid table with mutation slots",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Add: "add-item", Edit: "edit-item", Delete: "delete-item"},
			},
			cols: cols,
		},
		{
			name: "toggle slot on a table",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Toggle: "toggle-bought"},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "detail slot without rowid column",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Detail: "item-detail"},
			},
			cols:    []string{"item"},
			wantErr: true,
		},
		{
			name: "unknown mode",
			display: FormulaDisplay{
				Mode:    "gallery",
				Columns: []FormulaDisplayColumn{{Column: "item", Role: DisplayRoleTitle}},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name:    "empty mode",
			display: FormulaDisplay{Columns: []FormulaDisplayColumn{{Column: "item", Role: DisplayRoleTitle}}},
			cols:    cols,
			wantErr: true,
		},
		{
			name:    "missing title column",
			display: FormulaDisplay{Mode: DisplayModeChecklist},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "title column not in result",
			display: FormulaDisplay{
				Mode:    DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{{Column: "missing", Role: DisplayRoleTitle}},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "body column on checklist mode",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "check column on cards mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleCheck},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "body column not in result",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "missing", Role: DisplayRoleBody},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "row-bound slot without rowid column",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Delete: "delete-item"},
			},
			cols:    []string{"item"},
			wantErr: true,
		},
		{
			name: "reorder slot on a checklist",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols: cols,
		},
		{
			name: "reorder slot requires checklist mode",
			display: FormulaDisplay{
				Mode:  DisplayModeTable,
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "reorder slot rejected on a paginated view",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols: cols,
			params: ParamSchema{
				{Name: ReservedParamCursor, Type: ParamTypeInteger, Source: ParamSourcePagination},
				{Name: ReservedParamLimit, Type: ParamTypeInteger, Source: ParamSourcePagination},
			},
			wantErr: true,
		},
		{
			name: "reorder slot without rowid column",
			display: FormulaDisplay{
				Mode: DisplayModeChecklist,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols:    []string{"item"},
			wantErr: true,
		},
		{
			name: "valid board with move slot and body",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Slots: FormulaDisplaySlots{Move: "move-item"},
			},
			cols: cols,
		},
		{
			name: "read-only board without slots",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
			},
			cols: cols,
		},
		{
			name: "board without a group column",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "board rejects the toggle slot",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Slots: FormulaDisplaySlots{Toggle: "toggle-item"},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "move slot requires board mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Move: "move-item"},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "group column on cards mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "reorder slot on a board",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items", Move: "move-item"},
			},
			cols: cols,
		},
		{
			name: "reorder slot on cards mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols: cols,
		},
		{
			name: "reorder slot rejected on a paginated board",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Slots: FormulaDisplaySlots{Reorder: "reorder-items"},
			},
			cols: cols,
			params: ParamSchema{
				{Name: ReservedParamCursor, Type: ParamTypeInteger, Source: ParamSourcePagination},
				{Name: ReservedParamLimit, Type: ParamTypeInteger, Source: ParamSourcePagination},
			},
			wantErr: true,
		},
		{
			name: "move slot without rowid column",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Slots: FormulaDisplaySlots{Move: "move-item"},
			},
			cols:    []string{"item", "bought"},
			wantErr: true,
		},
		{
			name: "valid calendar with move, reorder, body, and the window pair",
			display: FormulaDisplay{
				Mode: DisplayModeCalendar,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "notes", Role: DisplayRoleBody},
					{Column: "bought", Role: DisplayRoleDay},
				},
				Slots: FormulaDisplaySlots{Add: "plan-item", Move: "move-item", Reorder: "reorder-items"},
			},
			cols:   cols,
			params: window,
		},
		{
			name: "calendar without a day column",
			display: FormulaDisplay{
				Mode:    DisplayModeCalendar,
				Columns: []FormulaDisplayColumn{{Column: "item", Role: DisplayRoleTitle}},
			},
			cols:    cols,
			params:  window,
			wantErr: true,
		},
		{
			name: "calendar without the window pair",
			display: FormulaDisplay{
				Mode: DisplayModeCalendar,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleDay},
				},
			},
			cols:    cols,
			wantErr: true,
		},
		{
			name: "calendar rejects the toggle slot",
			display: FormulaDisplay{
				Mode: DisplayModeCalendar,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleDay},
				},
				Slots: FormulaDisplaySlots{Toggle: "flip-item"},
			},
			cols:    cols,
			params:  window,
			wantErr: true,
		},
		{
			name: "calendar rejected on a paginated view",
			display: FormulaDisplay{
				Mode: DisplayModeCalendar,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleDay},
				},
			},
			cols: cols,
			params: ParamSchema{
				{Name: ReservedParamWindowStart, Type: ParamTypeDate, Source: ParamSourceWindow},
				{Name: ReservedParamWindowEnd, Type: ParamTypeDate, Source: ParamSourceWindow},
				{Name: ReservedParamCursor, Type: ParamTypeInteger, Source: ParamSourcePagination},
				{Name: ReservedParamLimit, Type: ParamTypeInteger, Source: ParamSourcePagination},
			},
			wantErr: true,
		},
		{
			name: "day role outside calendar mode",
			display: FormulaDisplay{
				Mode: DisplayModeCards,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleDay},
				},
			},
			cols:    cols,
			wantErr: true,
		},
	}

	calendar := func(window *FormulaDisplayWindow) FormulaDisplay {
		return FormulaDisplay{
			Mode: DisplayModeCalendar,
			Columns: []FormulaDisplayColumn{
				{Column: "item", Role: DisplayRoleTitle},
				{Column: "bought", Role: DisplayRoleDay},
			},
			Window: window,
		}
	}
	tests = append(tests, []struct {
		name     string
		display  FormulaDisplay
		cols     []string
		params   ParamSchema
		attached bool
		wantErr  bool
	}{
		{
			name:    "calendar with a Sunday-start weekly window",
			display: calendar(&FormulaDisplayWindow{WeekStart: DisplayWeekdaySunday, WeekEnd: DisplayWeekdaySaturday}),
			cols:    cols,
			params:  window,
		},
		{
			name:    "calendar with a wrapping weekend window",
			display: calendar(&FormulaDisplayWindow{WeekStart: DisplayWeekdaySaturday, WeekEnd: DisplayWeekdaySunday}),
			cols:    cols,
			params:  window,
		},
		{
			name:    "calendar with a work-week window naming only its end",
			display: calendar(&FormulaDisplayWindow{WeekEnd: DisplayWeekdayFriday}),
			cols:    cols,
			params:  window,
		},
		{
			name:    "calendar with a fixed range",
			display: calendar(&FormulaDisplayWindow{Start: "2026-09-18", Days: 4}),
			cols:    cols,
			params:  window,
		},
		{
			name:    "calendar window with an unknown weekday",
			display: calendar(&FormulaDisplayWindow{WeekStart: "lundi"}),
			cols:    cols,
			params:  window,
			wantErr: true,
		},
		{
			name:    "calendar window mixing both kinds",
			display: calendar(&FormulaDisplayWindow{WeekStart: DisplayWeekdayMonday, Start: "2026-09-18", Days: 4}),
			cols:    cols,
			params:  window,
			wantErr: true,
		},
		{
			name: "window outside calendar mode",
			display: FormulaDisplay{
				Mode: DisplayModeBoard,
				Columns: []FormulaDisplayColumn{
					{Column: "item", Role: DisplayRoleTitle},
					{Column: "bought", Role: DisplayRoleGroup},
				},
				Window: &FormulaDisplayWindow{WeekStart: DisplayWeekdaySunday},
			},
			cols:    cols,
			wantErr: true,
		},
	}...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormulaDisplay(tt.display, tt.cols, tt.params, tt.attached)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaDisplayInvalid) {
					t.Errorf("ValidateFormulaDisplay() error = %v, want ErrFormulaDisplayInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateFormulaDisplay() unexpected error: %v", err)
			}
		})
	}
}

// TestValidateFixedWindow pins what a fixed-range window accepts: a real day as its start, an absent or
// bounded length, and never a length on its own.
func TestValidateFixedWindow(t *testing.T) {
	tests := []struct {
		name    string
		window  FormulaDisplayWindow
		wantErr bool
	}{
		{name: "start and length", window: FormulaDisplayWindow{Start: "2026-09-18", Days: 4}},
		{name: "start alone", window: FormulaDisplayWindow{Start: "2026-09-18"}},
		{name: "one day", window: FormulaDisplayWindow{Start: "2026-09-18", Days: 1}},
		{name: "a whole month", window: FormulaDisplayWindow{Start: "2026-10-01", Days: MaxCalendarWindowDays}},
		{name: "length without a start", window: FormulaDisplayWindow{Days: 4}, wantErr: true},
		{name: "zero-padding missing", window: FormulaDisplayWindow{Start: "2026-9-18"}, wantErr: true},
		{name: "not a calendar day", window: FormulaDisplayWindow{Start: "2026-02-30"}, wantErr: true},
		{name: "a datetime", window: FormulaDisplayWindow{Start: "2026-09-18 10:00:00"}, wantErr: true},
		{name: "negative length", window: FormulaDisplayWindow{Start: "2026-09-18", Days: -1}, wantErr: true},
		{
			name:    "past the ceiling",
			window:  FormulaDisplayWindow{Start: "2026-09-18", Days: MaxCalendarWindowDays + 1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFixedWindow(tt.window)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaDisplayInvalid) {
					t.Errorf("validateFixedWindow() error = %v, want ErrFormulaDisplayInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("validateFixedWindow() unexpected error: %v", err)
			}
		})
	}
}

func TestValidateDisplaySlotAction(t *testing.T) {
	rowidParam := FormulaParam{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}
	parentParam := FormulaParam{
		Name: ReservedParamParentRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle,
	}
	requiredText := FormulaParam{Name: "item", Type: ParamTypeText, Source: ParamSourcePrompt}
	optionalText := FormulaParam{Name: "note", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true}
	lookupParam := FormulaParam{
		Name: "cat", Type: ParamTypeInteger, Source: ParamSourceLookup,
		Lookup: &LookupConfig{Table: "categories", LabelColumn: "name", ValueColumn: "id"},
	}
	bulkParam := FormulaParam{Name: ReservedParamRowIDs, Type: ParamTypeInteger, Source: ParamSourceSelectionBulk}
	orderedParam := FormulaParam{Name: ReservedParamOrderedRowIDs, Type: ParamTypeInteger, Source: ParamSourceReorder}
	optionalOrdered := FormulaParam{
		Name: ReservedParamOrderedRowIDs, Type: ParamTypeInteger, Source: ParamSourceReorder, Optional: true,
	}
	requiredChoice := FormulaParam{
		Name: "status", Type: ParamTypeText, Source: ParamSourcePrompt,
		Options: []FormulaOption{{Value: "todo"}, {Value: "done"}},
	}
	secondChoice := FormulaParam{
		Name: "lane", Type: ParamTypeText, Source: ParamSourcePrompt,
		Options: []FormulaOption{{Value: "a"}, {Value: "b"}},
	}

	action := func(verb StatementVerb, params ...FormulaParam) *Formula {
		return &Formula{Name: "the-action", Kind: FormulaKindAction, StatementVerb: verb, Params: params}
	}

	tests := []struct {
		name           string
		slot           DisplaySlot
		formula        *Formula
		parentAttached bool
		wantErr        bool
	}{
		{"attached add binding the parent row", DisplaySlotAdd,
			action(StatementVerbInsert, parentParam, requiredText), true, false},
		{"attached add without the parent binding", DisplaySlotAdd,
			action(StatementVerbInsert, requiredText), true, true},
		{"attached add binding the tapped row instead", DisplaySlotAdd,
			action(StatementVerbInsert, rowidParam, requiredText), true, true},
		{"attached delete binding its own row", DisplaySlotDelete,
			action(StatementVerbDelete, rowidParam), true, false},
		{"attached delete also binding the parent row", DisplaySlotDelete,
			action(StatementVerbDelete, rowidParam, parentParam), true, false},
		{"attached edit also binding the parent row", DisplaySlotEdit,
			action(StatementVerbUpdate, rowidParam, parentParam, requiredText), true, false},

		{"add insert with required prompt", DisplaySlotAdd, action(StatementVerbInsert, requiredText), false, false},
		{"add insert with lookup", DisplaySlotAdd,
			action(StatementVerbInsert, requiredText, lookupParam), false, false},
		{"add insert without params", DisplaySlotAdd, action(StatementVerbInsert), false, false},
		{"toggle update with rowid only", DisplaySlotToggle, action(StatementVerbUpdate, rowidParam), false, false},
		{"toggle with optional extra", DisplaySlotToggle,
			action(StatementVerbUpdate, rowidParam, optionalText), false, false},
		{"toggle composite recording the flip elsewhere (insert verb)", DisplaySlotToggle,
			action(StatementVerbInsert, rowidParam), false, false},
		{"toggle composite undoing its own record (delete verb)", DisplaySlotToggle,
			action(StatementVerbDelete, rowidParam), false, false},
		{"edit update with rowid and prompt", DisplaySlotEdit,
			action(StatementVerbUpdate, rowidParam, requiredText), false, false},
		{"edit composite rewrite of children (non-update verb)", DisplaySlotEdit,
			action(StatementVerbDelete, rowidParam, requiredText), false, false},
		{"delete with rowid only", DisplaySlotDelete, action(StatementVerbDelete, rowidParam), false, false},
		{"delete with optional extra", DisplaySlotDelete,
			action(StatementVerbDelete, rowidParam, optionalText), false, false},

		{"detail slot on a view", DisplaySlotDetail,
			&Formula{Name: "v", Kind: FormulaKindView, StatementVerb: StatementVerbSelect}, false, false},

		{"slot on a view", DisplaySlotAdd,
			&Formula{Name: "v", Kind: FormulaKindView, StatementVerb: StatementVerbSelect}, false, true},
		{"detail slot on an action", DisplaySlotDetail, action(StatementVerbDelete, rowidParam), false, true},
		{"add with wrong verb", DisplaySlotAdd, action(StatementVerbUpdate, requiredText), false, true},
		{"add binding a row selection", DisplaySlotAdd,
			action(StatementVerbInsert, rowidParam, requiredText), false, true},
		{"top-level add binding a parent row", DisplaySlotAdd,
			action(StatementVerbInsert, parentParam, requiredText), false, true},
		{"top-level toggle binding a parent row", DisplaySlotToggle,
			action(StatementVerbUpdate, rowidParam, parentParam), false, true},
		{"toggle missing rowid", DisplaySlotToggle, action(StatementVerbUpdate, optionalText), false, true},
		{"toggle with required extra", DisplaySlotToggle,
			action(StatementVerbUpdate, rowidParam, requiredText), false, true},
		{"edit without prompt param", DisplaySlotEdit, action(StatementVerbUpdate, rowidParam), false, true},
		{"delete with required extra", DisplaySlotDelete,
			action(StatementVerbDelete, rowidParam, requiredText), false, true},
		{"bulk selection param rejected", DisplaySlotDelete,
			action(StatementVerbDelete, rowidParam, bulkParam), false, true},
		{"unknown slot", DisplaySlot("archive"), action(StatementVerbUpdate, rowidParam), false, true},

		{"bulk entry with rowids only", DisplaySlotBulk, action(StatementVerbDelete, bulkParam), false, false},
		{"bulk entry any verb with required prompt", DisplaySlotBulk,
			action(StatementVerbUpdate, bulkParam, requiredText), false, false},
		{"bulk entry missing rowids", DisplaySlotBulk, action(StatementVerbDelete, requiredText), false, true},
		{"bulk entry binding a single row", DisplaySlotBulk,
			action(StatementVerbDelete, bulkParam, rowidParam), false, true},
		{"row_actions entry with insert verb", DisplaySlotRowActions,
			action(StatementVerbInsert, rowidParam, requiredText), false, false},
		{"row_actions entry missing rowid", DisplaySlotRowActions,
			action(StatementVerbUpdate, requiredText), false, true},
		{"row_actions entry binding a bulk selection", DisplaySlotRowActions,
			action(StatementVerbDelete, rowidParam, bulkParam), false, true},
		{"detail-context row_actions binding the parent row", DisplaySlotRowActions,
			action(StatementVerbUpdate, rowidParam, parentParam), true, false},

		{"reorder update with the row order only", DisplaySlotReorder,
			action(StatementVerbUpdate, orderedParam), false, false},
		{"reorder with optional extra", DisplaySlotReorder,
			action(StatementVerbUpdate, orderedParam, optionalText), false, false},
		{"detail-context reorder binding the parent row", DisplaySlotReorder,
			action(StatementVerbUpdate, orderedParam, parentParam), true, false},
		{"reorder with non-update verb", DisplaySlotReorder,
			action(StatementVerbDelete, orderedParam), false, true},
		{"reorder missing the row order", DisplaySlotReorder,
			action(StatementVerbUpdate, optionalText), false, true},
		{"reorder with required extra", DisplaySlotReorder,
			action(StatementVerbUpdate, orderedParam, requiredText), false, true},
		{"reorder binding a single row", DisplaySlotReorder,
			action(StatementVerbUpdate, orderedParam, rowidParam), false, true},
		{"row order param outside the reorder slot", DisplaySlotRowActions,
			action(StatementVerbUpdate, rowidParam, orderedParam), false, true},

		{"move update with rowid and the target", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice), false, false},
		{"move with optional extra", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, optionalText), false, false},
		{"detail-context move binding the parent row", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, parentParam), true, false},
		{"move with non-update verb", DisplaySlotMove,
			action(StatementVerbDelete, rowidParam, requiredChoice), false, true},
		{"move missing rowid", DisplaySlotMove,
			action(StatementVerbUpdate, requiredChoice), false, true},
		{"move missing the target", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, optionalText), false, true},
		{"move with a required target lacking options", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredText), false, true},
		{"move with two required option params", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, secondChoice), false, true},
		{"move with a required extra beyond the target", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, requiredText), false, true},
		{"move also binding an optional row order", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, optionalOrdered), false, false},
		{"move binding a required row order", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, requiredChoice, orderedParam), false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every case here is a board or list surface; the calendar shapes have their own table.
			mode := DisplayModeChecklist
			if tt.slot == DisplaySlotMove {
				mode = DisplayModeBoard
			}
			err := ValidateDisplaySlotAction(tt.slot, mode, tt.formula, tt.parentAttached)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaDisplayInvalid) {
					t.Errorf("ValidateDisplaySlotAction() error = %v, want ErrFormulaDisplayInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateDisplaySlotAction() unexpected error: %v", err)
			}
		})
	}
}

// TestValidateDisplaySlotAction_Calendar pins the shapes a calendar's slots take: a move binds :rowid plus
// exactly one required date target without options (and may carry the optional row order), and an add
// may declare at most one date-typed prompt parameter — the one a day's "+" fills.
func TestValidateDisplaySlotAction_Calendar(t *testing.T) {
	rowidParam := FormulaParam{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}
	dayTarget := FormulaParam{Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt}
	otherDay := FormulaParam{Name: "other", Type: ParamTypeDate, Source: ParamSourcePrompt}
	optionalDay := FormulaParam{Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt, Optional: true}
	optionalNote := FormulaParam{Name: "note", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true}
	requiredName := FormulaParam{Name: "name", Type: ParamTypeText, Source: ParamSourcePrompt}
	optionalOrdered := FormulaParam{
		Name: ReservedParamOrderedRowIDs, Type: ParamTypeInteger, Source: ParamSourceReorder, Optional: true,
	}
	action := func(verb StatementVerb, params ...FormulaParam) *Formula {
		return &Formula{Name: "the-action", Kind: FormulaKindAction, StatementVerb: verb, Params: params}
	}

	tests := []struct {
		name    string
		slot    DisplaySlot
		formula *Formula
		wantErr bool
	}{
		{"move with rowid and a date target", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, dayTarget), false},
		{"move with an optional extra and the row order", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, dayTarget, optionalNote, optionalOrdered), false},
		{"move missing the target", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, optionalNote), true},
		{"move with two required targets", DisplaySlotMove,
			action(StatementVerbUpdate, rowidParam, dayTarget, otherDay), true},
		{"add with one date parameter", DisplaySlotAdd,
			action(StatementVerbInsert, requiredName, optionalDay), false},
		{"add with a required date parameter", DisplaySlotAdd,
			action(StatementVerbInsert, requiredName, dayTarget), false},
		{"add without a date parameter", DisplaySlotAdd,
			action(StatementVerbInsert, requiredName), false},
		{"add with two date parameters", DisplaySlotAdd,
			action(StatementVerbInsert, requiredName, optionalDay, otherDay), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDisplaySlotAction(tt.slot, DisplayModeCalendar, tt.formula, false)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaDisplayInvalid) {
					t.Errorf("ValidateDisplaySlotAction() error = %v, want ErrFormulaDisplayInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateDisplaySlotAction() unexpected error: %v", err)
			}
		})
	}
}

// TestValidateCalendarMoveTarget pins what a calendar drop can bind the destination day to: a date
// parameter without options passes; a text parameter, an integer one, and a date parameter carrying
// options are each refused with the display sentinel.
func TestValidateCalendarMoveTarget(t *testing.T) {
	action := &Formula{Name: "move-meal", Kind: FormulaKindAction, StatementVerb: StatementVerbUpdate}
	tests := []struct {
		name    string
		param   FormulaParam
		wantErr bool
	}{
		{"a bare date parameter", FormulaParam{Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt}, false},
		{"a text parameter", FormulaParam{Name: "day", Type: ParamTypeText, Source: ParamSourcePrompt}, true},
		{"an integer parameter", FormulaParam{Name: "day", Type: ParamTypeInteger, Source: ParamSourcePrompt}, true},
		{"a date parameter with options", FormulaParam{Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt,
			Options: []FormulaOption{{Value: "2026-09-07", Label: "today"}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCalendarMoveTarget(DisplaySlotMove, action, tt.param)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaDisplayInvalid) {
					t.Errorf("validateCalendarMoveTarget() error = %v, want ErrFormulaDisplayInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("validateCalendarMoveTarget() unexpected error: %v", err)
			}
		})
	}
}

func TestFormulaDisplaySlots_Slots(t *testing.T) {
	t.Run("yields filled slots in declaration order", func(t *testing.T) {
		actions := FormulaDisplaySlots{
			Add:        "add-item",
			Edit:       "edit-item",
			Delete:     "delete-item",
			Bulk:       []string{"archive-selected", "delete-selected"},
			RowActions: []string{"duplicate-item"},
		}

		var slots []DisplaySlot
		var refs []string
		for slot, ref := range actions.All() {
			slots = append(slots, slot)
			refs = append(refs, ref)
		}

		wantSlots := []DisplaySlot{
			DisplaySlotAdd, DisplaySlotEdit, DisplaySlotDelete,
			DisplaySlotBulk, DisplaySlotBulk, DisplaySlotRowActions,
		}
		wantRefs := []string{"add-item", "edit-item", "delete-item",
			"archive-selected", "delete-selected", "duplicate-item"}
		if !slices.Equal(slots, wantSlots) {
			t.Errorf("slots = %v, want %v", slots, wantSlots)
		}
		if !slices.Equal(refs, wantRefs) {
			t.Errorf("refs = %v, want %v", refs, wantRefs)
		}
	})

	t.Run("IsEmpty sees a list-only fill", func(t *testing.T) {
		if !(FormulaDisplaySlots{}).IsEmpty() {
			t.Error("IsEmpty() = false for the zero value, want true")
		}
		if (FormulaDisplaySlots{Bulk: []string{"archive-selected"}}).IsEmpty() {
			t.Error("IsEmpty() = true with a bulk entry, want false")
		}
	})

	t.Run("yields nothing when no slot is filled", func(t *testing.T) {
		for slot, ref := range (FormulaDisplaySlots{}).All() {
			t.Errorf("unexpected yield (%q, %q)", slot, ref)
		}
	})

	t.Run("stops when the consumer breaks", func(t *testing.T) {
		actions := FormulaDisplaySlots{Toggle: "toggle-bought", Delete: "delete-item"}

		var got []DisplaySlot
		for slot := range actions.All() {
			got = append(got, slot)
			break
		}

		want := []DisplaySlot{DisplaySlotToggle}
		if !slices.Equal(got, want) {
			t.Errorf("slots seen before break = %v, want %v", got, want)
		}
	})
}
