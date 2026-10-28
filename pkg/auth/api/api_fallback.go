package api

import "net/http"

type apiFallbackData struct {
	MCPURL string
}

// GetAPIFallback renders the API root fallback page with the MCP endpoint URL.
func (h *Handler) GetAPIFallback(w http.ResponseWriter, _ *http.Request) {
	h.render(w, "api-fallback.html", apiFallbackData{
		MCPURL: h.publicURL + "/mcp",
	})
}
