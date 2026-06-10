// Package conversations implements a RouteModuleV2 that adds stateful chat
// endpoints on top of the stateless proxy. It persists conversation history via
// internal/conversation and replays it through the existing execution path
// (BaseAPIHandler.ExecuteWithAuthManager), so no provider-specific code or
// translator logic needs to change.
package conversations

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/conversation"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	claudehandler "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
)

// Module implements modules.RouteModuleV2 for the conversations feature.
type Module struct {
	accessManager  *sdkaccess.Manager
	authMiddleware gin.HandlerFunc
	base           *handlers.BaseAPIHandler
	claude         *claudehandler.ClaudeCodeAPIHandler
	registerOnce   sync.Once

	mu               sync.RWMutex
	enabled          bool
	store            conversation.Store
	dir              string
	defaultMaxTokens int
}

// New creates a conversations module. The access manager is currently unused
// directly (auth is enforced via authMiddleware) but kept for symmetry with
// other modules and potential future per-principal logic.
func New(accessManager *sdkaccess.Manager, authMiddleware gin.HandlerFunc) *Module {
	return &Module{accessManager: accessManager, authMiddleware: authMiddleware}
}

// Name returns the module identifier.
func (m *Module) Name() string { return "conversations" }

// Register wires the conversation routes into the engine. It is idempotent.
func (m *Module) Register(ctx modules.Context) error {
	m.base = ctx.BaseHandler
	if m.base != nil {
		m.claude = claudehandler.NewClaudeCodeAPIHandler(m.base)
	}
	m.applyConfig(ctx.Config)

	m.registerOnce.Do(func() {
		group := ctx.Engine.Group("/v1/conversations")
		if m.authMiddleware != nil {
			group.Use(m.authMiddleware)
		}
		group.Use(m.availabilityMiddleware())
		group.POST("", m.handleCreate)
		group.GET("", m.handleList)
		group.GET("/:id", m.handleGet)
		group.POST("/:id/messages", m.handleContinue)
		group.DELETE("/:id", m.handleDelete)
	})
	return nil
}

// OnConfigUpdated refreshes cached settings on hot reload.
func (m *Module) OnConfigUpdated(cfg *config.Config) error {
	m.applyConfig(cfg)
	return nil
}

// applyConfig updates the enabled flag, token budget, and store (recreating the
// store only when the directory changes).
func (m *Module) applyConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = cfg.Conversations.Enabled
	m.defaultMaxTokens = cfg.Conversations.DefaultMaxTokens
	if m.defaultMaxTokens <= 0 {
		m.defaultMaxTokens = config.DefaultConversationsMaxTokens
	}
	dir := cfg.Conversations.Dir
	if dir == "" {
		dir = config.DefaultConversationsDir
	}
	if m.store == nil || m.dir != dir {
		m.dir = dir
		m.store = conversation.NewFileStore(dir)
	}
}

// availabilityMiddleware returns 404 when the feature is disabled or not ready,
// mirroring how disabled features behave elsewhere in the server.
func (m *Module) availabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		m.mu.RLock()
		ready := m.enabled && m.store != nil && m.base != nil
		m.mu.RUnlock()
		if !ready {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "conversations feature is disabled"})
			return
		}
		c.Next()
	}
}

// snapshot returns the current store and default token budget under lock.
func (m *Module) snapshot() (conversation.Store, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store, m.defaultMaxTokens
}
