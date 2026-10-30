package service

import "context"

// manualRenderer renders the databases app's built-in manuals.
type manualRenderer interface {
	// RenderManual renders the manual for topic, each operation named by its command, quoting the limits the account
	// works under. An unknown topic is an error naming the topics.
	RenderManual(ctx context.Context, topic string) (string, error)
}

// DocsService serves the built-in manuals.
type DocsService struct {
	manuals manualRenderer
}

// NewDocsService creates a DocsService rendering the manuals of the databases app.
func NewDocsService(manuals manualRenderer) *DocsService {
	return &DocsService{manuals: manuals}
}

// Manual returns the manual for topic.
func (s *DocsService) Manual(ctx context.Context, topic string) (string, error) {
	return s.manuals.RenderManual(ctx, topic)
}
