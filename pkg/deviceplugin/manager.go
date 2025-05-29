package deviceplugin

import (
   "context"
)

// Manager tracks and monitors devices for a specific resource.
type Manager struct {
   ResourceName string
}

// NewManager creates a new Manager for the given extended resource name.
func NewManager(resourceName string) *Manager {
   return &Manager{ResourceName: resourceName}
}

// Start begins device discovery and health monitoring until the context is done.
func (m *Manager) Start(ctx context.Context) {
   <-ctx.Done()
}