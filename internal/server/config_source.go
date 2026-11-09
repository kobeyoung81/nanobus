package server

import (
	"context"
	"fmt"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// configItems reads one component's configuration from the admin module.
//
// The bus serves messages; configuration belongs to admin. The two places the
// bus still needs it — the voice endpoint it exposes and the agent config
// snapshot it answers — ask over HTTP like every other module does.
func (s *Server) configItems(ctx context.Context, component string) ([]store.ConfigItem, error) {
	if s.admin == nil || !s.admin.Enabled() {
		return nil, fmt.Errorf("admin module is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.admin.Items(ctx, component)
}
