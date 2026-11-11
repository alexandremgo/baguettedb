package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// DocsHandler serves the built-in manuals over REST.
type DocsHandler struct {
	docs   *service.DocsService
	logger zerolog.Logger
}

// NewDocsHandler creates a DocsHandler rendering manuals with docs.
func NewDocsHandler(docs *service.DocsService, logger zerolog.Logger) *DocsHandler {
	return &DocsHandler{docs: docs, logger: logger}
}

// RegisterDocsRoutes mounts the manual route on r, behind requireToken: any signed-in user may read a manual.
func RegisterDocsRoutes(r chi.Router, h *DocsHandler, requireToken func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Get("/api/databases/v1/docs/{topic}", h.GetManual)
	})
}

// manualResponse is a built-in manual, in Markdown.
type manualResponse struct {
	Topic   string `json:"topic"`
	Content string `json:"content"`
}

// GetManual handles GET /api/databases/v1/docs/{topic}: the manual of the topic, rendered as get_docs renders it, with
// the caller's limits and each operation named by its MCP tool. An unknown topic is a 404 naming the topics.
func (h *DocsHandler) GetManual(w http.ResponseWriter, r *http.Request) {
	topic := chi.URLParam(r, "topic")
	topics := service.ManualTopics()
	if !slices.Contains(topics, topic) {
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{
			Error: "no manual " + topic + "; the topics are " + strings.Join(topics, ", "),
		})
		return
	}
	content, err := h.docs.Manual(r.Context(), topic, service.ToolNames)
	if err != nil {
		h.logger.Error().Err(err).Str("topic", topic).Msg("failed to render a manual")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	httputil.RespondJSON(w, http.StatusOK, manualResponse{Topic: topic, Content: content})
}
