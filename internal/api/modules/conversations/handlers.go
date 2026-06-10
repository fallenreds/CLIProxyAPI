package conversations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/conversation"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// anonymousOwner is used when no API key is configured (open server). It keeps
// the feature usable while still namespacing storage.
const anonymousOwner = "_anonymous"

// ownerOf resolves the storage owner from the authenticated client API key.
func ownerOf(c *gin.Context) string {
	if v, ok := c.Get("userApiKey"); ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return anonymousOwner
}

// handleCreate creates a conversation and, if an initial message is supplied,
// runs the first turn and returns the assistant reply.
func (m *Module) handleCreate(c *gin.Context) {
	store, defMax := m.snapshot()
	raw, err := c.GetRawData()
	if err != nil {
		badRequest(c, "cannot read request body")
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(raw, "model").String())
	if model == "" {
		badRequest(c, "model is required")
		return
	}
	conv := &conversation.Conversation{
		Owner:    ownerOf(c),
		Title:    gjson.GetBytes(raw, "title").String(),
		Model:    model,
		System:   gjson.GetBytes(raw, "system").String(),
		Protocol: "claude",
		Messages: []conversation.Message{},
	}

	msg := gjson.GetBytes(raw, "message")
	hasMsg := msg.Exists() && msg.Raw != "" && msg.Raw != "null"
	if hasMsg {
		conv.Messages = append(conv.Messages, conversation.Message{
			Role:    "user",
			Content: json.RawMessage(msg.Raw),
		})
	}

	created, err := store.Create(c.Request.Context(), conv)
	if err != nil {
		internalError(c, err)
		return
	}

	if !hasMsg {
		c.JSON(http.StatusCreated, gin.H{
			"id":       created.ID,
			"model":    created.Model,
			"title":    created.Title,
			"messages": created.Messages,
		})
		return
	}

	maxTokens := int(gjson.GetBytes(raw, "max_tokens").Int())
	m.runTurn(c, store, created, maxTokens, defMax, http.StatusCreated)
}

// handleContinue appends a user message and runs the next turn.
func (m *Module) handleContinue(c *gin.Context) {
	store, defMax := m.snapshot()
	conv, err := store.Get(c.Request.Context(), ownerOf(c), c.Param("id"))
	if err == conversation.ErrNotFound {
		notFound(c)
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	raw, err := c.GetRawData()
	if err != nil {
		badRequest(c, "cannot read request body")
		return
	}
	content := gjson.GetBytes(raw, "content")
	if !content.Exists() || content.Raw == "" || content.Raw == "null" {
		badRequest(c, "content is required")
		return
	}
	conv.Messages = append(conv.Messages, conversation.Message{
		Role:    "user",
		Content: json.RawMessage(content.Raw),
	})
	maxTokens := int(gjson.GetBytes(raw, "max_tokens").Int())
	m.runTurn(c, store, conv, maxTokens, defMax, http.StatusOK)
}

// handleGet returns the full conversation (resume).
func (m *Module) handleGet(c *gin.Context) {
	store, _ := m.snapshot()
	conv, err := store.Get(c.Request.Context(), ownerOf(c), c.Param("id"))
	if err == conversation.ErrNotFound {
		notFound(c)
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, conv)
}

// handleList returns lightweight summaries of the caller's conversations.
func (m *Module) handleList(c *gin.Context) {
	store, _ := m.snapshot()
	list, err := store.List(c.Request.Context(), ownerOf(c))
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"conversations": list})
}

// handleDelete removes a conversation.
func (m *Module) handleDelete(c *gin.Context) {
	store, _ := m.snapshot()
	id := c.Param("id")
	err := store.Delete(c.Request.Context(), ownerOf(c), id)
	if err == conversation.ErrNotFound {
		notFound(c)
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": id})
}

// runTurn replays the conversation through the standard execution path, appends
// the assistant reply, persists it, and writes the upstream response (with the
// conversation id injected) back to the client.
func (m *Module) runTurn(c *gin.Context, store conversation.Store, conv *conversation.Conversation, reqMax, defMax, successStatus int) {
	maxTokens := reqMax
	if maxTokens <= 0 {
		maxTokens = defMax
	}
	payload, err := buildPayload(conv, maxTokens)
	if err != nil {
		internalError(c, err)
		return
	}

	cliCtx, cancel := m.base.GetContextWithCancel(m.claude, c, context.Background())
	defer cancel()

	resp, _, errMsg := m.base.ExecuteWithAuthManager(cliCtx, m.claude.HandlerType(), conv.Model, payload, "")
	if errMsg != nil {
		status := errMsg.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		message := "upstream error"
		if errMsg.Error != nil {
			message = errMsg.Error.Error()
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	content := gjson.GetBytes(resp, "content")
	assistant := conversation.Message{Role: "assistant", Content: json.RawMessage("[]")}
	if content.Exists() && content.Raw != "" {
		assistant.Content = json.RawMessage(content.Raw)
	}
	conv.Messages = append(conv.Messages, assistant)
	if err = store.Save(c.Request.Context(), conv); err != nil {
		internalError(c, err)
		return
	}

	out, errSet := sjson.SetBytes(resp, "conversation_id", conv.ID)
	if errSet != nil {
		out = resp
	}
	c.Header("Content-Type", "application/json")
	c.Status(successStatus)
	_, _ = c.Writer.Write(out)
}

// buildPayload constructs an Anthropic /v1/messages request body from the stored
// conversation. messages round-trip as the Anthropic role/content shape.
func buildPayload(conv *conversation.Conversation, maxTokens int) ([]byte, error) {
	payload := map[string]any{
		"model":      conv.Model,
		"max_tokens": maxTokens,
		"messages":   conv.Messages,
		"stream":     false,
	}
	if strings.TrimSpace(conv.System) != "" {
		payload["system"] = conv.System
	}
	return json.Marshal(payload)
}

func badRequest(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": message})
}

func notFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
}

func internalError(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}
