//go:build integration

package integrationtest

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// unicodeEscapePrefix starts the escape sequence encoding/json writes for <, > and & by default: a backslash, then
// "u00".
var unicodeEscapePrefix = string(rune(92)) + "u00"

// TestMCP_Results_KeepHTMLCharacters checks that a tool result carries <, > and & as the stored value holds them,
// not as unicode escape sequences, in the rows of a read and in the logged values of a write.
func TestMCP_Results_KeepHTMLCharacters(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)
	const body = "> quote <b>R&D</b>"

	written := toolText(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "UPDATE notes SET body = '" + body + "' WHERE id = 2",
	})
	read := toolText(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID, "sql": "SELECT body FROM notes WHERE id = 2",
	})
	for tool, text := range map[string]string{"execute_data": written, "execute_query": read} {
		if !strings.Contains(text, body) || strings.Contains(text, unicodeEscapePrefix) {
			t.Errorf("%s result = %s, want it to carry %q as it is", tool, text, body)
		}
	}
}
