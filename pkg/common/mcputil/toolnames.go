package mcputil

// MCP tool names, shared by every module that registers or cross-references a tool. Registrations and the
// cross-tool mentions inside tool descriptions must both use these constants, so a rename can never leave a
// description pointing at a tool that no longer exists.
const (
	ToolListBdbs    = "list_bdbs"
	ToolDescribeBdb = "describe_bdb"
	ToolUpdateBdb   = "update_bdb"

	ToolListStarters = "list_starters"
	ToolCreateBdb    = "create_bdb"

	ToolDescribeTable = "describe_table"
	ToolExecuteQuery  = "execute_query"
	ToolExecuteData   = "execute_data"
	ToolExecuteSchema = "execute_schema"

	ToolListChanges          = "list_changes"
	ToolUndoLastChange       = "undo_last_change"
	ToolRedoLastChange       = "redo_last_change"
	ToolUndoLastChangeAnyone = "undo_last_change_anyone"
	ToolRedoLastChangeAnyone = "redo_last_change_anyone"
	ToolRevertChange         = "revert_change"

	ToolCreateSkill = "create_skill"
	ToolUpdateSkill = "update_skill"
	ToolGetSkill    = "get_skill"

	ToolCreateFormula = "create_formula"
	ToolUpdateFormula = "update_formula"
	ToolGetFormula    = "get_formula"
	ToolListFormulas  = "list_formulas"
	ToolDeleteFormula = "delete_formula"
	ToolRunFormula    = "run_formula"

	ToolGetDocs = "get_docs"
)
