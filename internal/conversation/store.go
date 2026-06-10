// Package conversation provides server-side persistence of chat conversations,
// enabling stateful "chat" semantics (create, list, resume, continue, delete)
// on top of the otherwise stateless proxy. Conversations are scoped per client
// API key (owner) so different callers cannot see each other's history.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when a conversation does not exist for the owner.
var ErrNotFound = errors.New("conversation not found")

// Message is a single turn in a conversation, stored in the Anthropic
// "messages" shape. Content is kept as raw JSON so both plain-string and
// structured content-block payloads round-trip unchanged.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Conversation is the persisted chat document.
type Conversation struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Title     string    `json:"title,omitempty"`
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Protocol  string    `json:"protocol"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Summary is the lightweight projection returned by List (no message bodies).
type Summary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Model        string    `json:"model"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Store persists conversations for owners. Implementations must be safe for
// concurrent use.
type Store interface {
	// Create persists a new conversation and returns it.
	Create(ctx context.Context, conv *Conversation) (*Conversation, error)
	// Get returns the conversation identified by id for the given owner.
	// It returns ErrNotFound when the conversation does not exist.
	Get(ctx context.Context, owner, id string) (*Conversation, error)
	// List returns summaries of all conversations owned by owner, newest first.
	List(ctx context.Context, owner string) ([]Summary, error)
	// Save replaces the stored conversation with the provided one.
	Save(ctx context.Context, conv *Conversation) error
	// Delete removes the conversation identified by id for the given owner.
	// It returns ErrNotFound when the conversation does not exist.
	Delete(ctx context.Context, owner, id string) error
}
