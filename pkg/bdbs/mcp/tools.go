package mcp

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

var tracer = otel.Tracer("baguettedb/bdbs/mcp")

// DatabasesAppRef holds a lazily-injected reference to DatabasesApp.
// Populated after the databases module is set up.
type DatabasesAppRef struct {
	App service.DatabasesApp
}

// parseUserID extracts and parses the user ID from the MCP context.
func parseUserID(ctx context.Context) (uuid.UUID, error) {
	sub, ok := mcputil.UserIDFromContext(ctx)
	if !ok {
		return uuid.Nil, errors.New("missing user identity")
	}

	userID, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, errors.New("invalid user identity")
	}

	return userID, nil
}
