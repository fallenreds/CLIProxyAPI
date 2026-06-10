package conversation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileStore is a file-based Store implementation. Each conversation is stored
// as a single JSON document at <root>/<ownerHash>/<id>.json. It requires no
// external dependencies and survives restarts, which suits single-instance
// deployments. Writes are atomic (temp file + rename) and guarded by a mutex.
type FileStore struct {
	root string
	mu   sync.RWMutex
}

// NewFileStore creates a FileStore rooted at the given directory. The directory
// is created on demand during writes.
func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

// NewID returns a new conversation identifier.
func NewID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to a timestamp-based identifier; collisions are unlikely
		// for a single owner and this path is effectively unreachable.
		return fmt.Sprintf("conv_%d", time.Now().UnixNano())
	}
	return "conv_" + hex.EncodeToString(buf)
}

// ownerHash maps an owner (client API key) to a filesystem-safe directory name.
func ownerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])
}

func (s *FileStore) ownerDir(owner string) string {
	return filepath.Join(s.root, ownerHash(owner))
}

func (s *FileStore) convPath(owner, id string) string {
	return filepath.Join(s.ownerDir(owner), id+".json")
}

// validID guards against path traversal via the id path segment.
func validID(id string) bool {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return false
	}
	return true
}

func (s *FileStore) Create(ctx context.Context, conv *Conversation) (*Conversation, error) {
	if conv.ID == "" {
		conv.ID = NewID()
	}
	if !validID(conv.ID) {
		return nil, fmt.Errorf("invalid conversation id")
	}
	now := time.Now().UTC()
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = now
	}
	conv.UpdatedAt = now
	if err := s.write(conv); err != nil {
		return nil, err
	}
	return conv, nil
}

func (s *FileStore) Get(ctx context.Context, owner, id string) (*Conversation, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLocked(owner, id)
}

func (s *FileStore) List(ctx context.Context, owner string) ([]Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.ownerDir(owner)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Summary{}, nil
		}
		return nil, err
	}

	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		conv, errRead := s.readLocked(owner, id)
		if errRead != nil {
			continue
		}
		summaries = append(summaries, Summary{
			ID:           conv.ID,
			Title:        conv.Title,
			Model:        conv.Model,
			MessageCount: len(conv.Messages),
			CreatedAt:    conv.CreatedAt,
			UpdatedAt:    conv.UpdatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries, nil
}

func (s *FileStore) Save(ctx context.Context, conv *Conversation) error {
	if !validID(conv.ID) {
		return fmt.Errorf("invalid conversation id")
	}
	conv.UpdatedAt = time.Now().UTC()
	return s.write(conv)
}

func (s *FileStore) Delete(ctx context.Context, owner, id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.convPath(owner, id)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.Remove(path)
}

func (s *FileStore) readLocked(owner, id string) (*Conversation, error) {
	data, err := os.ReadFile(s.convPath(owner, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var conv Conversation
	if err = json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("decode conversation %s: %w", id, err)
	}
	return &conv, nil
}

// write persists the conversation atomically: it serializes to a temp file in
// the owner directory and renames it over the target path.
func (s *FileStore) write(conv *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.ownerDir(conv.Owner)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conversation dir: %w", err)
	}
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return fmt.Errorf("encode conversation: %w", err)
	}
	tmp, err := os.CreateTemp(dir, conv.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename did not consume the temp file.
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, s.convPath(conv.Owner, conv.ID)); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
