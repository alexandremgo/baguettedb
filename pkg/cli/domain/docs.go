package domain

import "github.com/alexandremgo/baguettedb/pkg/common/mcputil"

// toolCommands maps each MCP tool to the command that does the same.
var toolCommands = map[string]string{
	mcputil.ToolListBdbs:             "bdb bdbs list",
	mcputil.ToolDescribeBdb:          "bdb bdbs describe",
	mcputil.ToolUpdateBdb:            "bdb bdbs update",
	mcputil.ToolListStarters:         "bdb starters",
	mcputil.ToolCreateBdb:            "bdb bdbs create",
	mcputil.ToolDescribeTable:        "bdb bdbs describe",
	mcputil.ToolExecuteQuery:         "bdb sql query",
	mcputil.ToolExecuteData:          "bdb sql data",
	mcputil.ToolExecuteSchema:        "bdb sql schema",
	mcputil.ToolListChanges:          "bdb changes list",
	mcputil.ToolUndoLastChange:       "bdb changes undo",
	mcputil.ToolRedoLastChange:       "bdb changes redo",
	mcputil.ToolUndoLastChangeAnyone: "bdb changes undo --anyone",
	mcputil.ToolRedoLastChangeAnyone: "bdb changes redo --anyone",
	mcputil.ToolRevertChange:         "bdb changes revert",
	mcputil.ToolCreateSkill:          "bdb skills create",
	mcputil.ToolUpdateSkill:          "bdb skills update",
	mcputil.ToolGetSkill:             "bdb skills show",
	mcputil.ToolCreateFormula:        "bdb formulas create",
	mcputil.ToolUpdateFormula:        "bdb formulas update",
	mcputil.ToolGetFormula:           "bdb formulas show",
	mcputil.ToolListFormulas:         "bdb formulas list",
	mcputil.ToolDeleteFormula:        "bdb formulas delete",
	mcputil.ToolRunFormula:           "bdb formulas run",
	mcputil.ToolGetDocs:              "bdb docs",
}

// CommandForTool returns the command that does what the MCP tool tool does, for text written about tools, such as the
// built-in manuals, to name the command instead; a tool with no command keeps its name.
func CommandForTool(tool string) string {
	if command, ok := toolCommands[tool]; ok {
		return command
	}
	return tool
}
