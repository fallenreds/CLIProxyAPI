package conversation

import (
	"context"
	"encoding/json"
	"testing"
)

func msg(role, text string) Message {
	content, _ := json.Marshal(text)
	return Message{Role: role, Content: content}
}

func TestFileStoreCreateGetSaveDelete(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()
	const owner = "key-abc"

	created, err := store.Create(ctx, &Conversation{
		Owner:    owner,
		Model:    "claude-sonnet-4-5-20250929",
		Protocol: "claude",
		Messages: []Message{msg("user", "hello")},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("expected timestamps to be set")
	}

	got, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 1 || got.Model != created.Model {
		t.Fatalf("unexpected conversation: %+v", got)
	}

	got.Messages = append(got.Messages, msg("assistant", "hi"))
	if err = store.Save(ctx, got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get after save: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(reloaded.Messages))
	}

	if err = store.Delete(ctx, owner, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err = store.Get(ctx, owner, created.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err = store.Delete(ctx, owner, created.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestFileStoreListIsolatedByOwner(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Create(ctx, &Conversation{Owner: "owner-a", Model: "m", Protocol: "claude"}); err != nil {
			t.Fatalf("Create a: %v", err)
		}
	}
	if _, err := store.Create(ctx, &Conversation{Owner: "owner-b", Model: "m", Protocol: "claude"}); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	listA, err := store.List(ctx, "owner-a")
	if err != nil {
		t.Fatalf("List a: %v", err)
	}
	if len(listA) != 3 {
		t.Fatalf("owner-a expected 3 conversations, got %d", len(listA))
	}

	listB, err := store.List(ctx, "owner-b")
	if err != nil {
		t.Fatalf("List b: %v", err)
	}
	if len(listB) != 1 {
		t.Fatalf("owner-b expected 1 conversation, got %d", len(listB))
	}

	empty, err := store.List(ctx, "owner-none")
	if err != nil {
		t.Fatalf("List none: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty list, got %d", len(empty))
	}
}

func TestFileStoreGetNotFoundAndInvalidID(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()

	if _, err := store.Get(ctx, "owner", "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := store.Get(ctx, "owner", "../escape"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for traversal id, got %v", err)
	}
}
