package ids

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Registry maps full keys to stable short IDs (e.g. "trc:a7").
// Persists to a JSON file between runs.
type Registry struct {
	entries map[string]string
	reverse map[string]string
	path    string
	mu      sync.RWMutex
}

// NewRegistry creates a Registry backed by the given file path.
func NewRegistry(path string) *Registry {
	return &Registry{
		entries: make(map[string]string),
		reverse: make(map[string]string),
		path:    path,
	}
}

// Load reads persisted IDs from disk. No-op if the file doesn't exist.
func (r *Registry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(filepath.Clean(r.path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading ids file: %w", err)
	}

	var stored map[string]string
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parsing ids file: %w", err)
	}

	r.entries = stored
	r.reverse = make(map[string]string, len(stored))
	for k, v := range stored {
		r.reverse[v] = k
	}
	return nil
}

// Save writes the registry to disk, creating directories as needed.
func (r *Registry) Save() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
		return fmt.Errorf("creating ids directory: %w", err)
	}

	data, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding ids: %w", err)
	}
	return os.WriteFile(filepath.Clean(r.path), data, 0o600)
}

// GetOrCreate returns the stable short ID for the given prefix+key.
func (r *Registry) GetOrCreate(prefix, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	fullKey := prefix + ":" + key
	if id, ok := r.entries[fullKey]; ok {
		return id
	}

	id := r.generate(prefix, key)
	r.entries[fullKey] = id
	r.reverse[id] = fullKey
	return id
}

// Resolve returns the original key for a short ID (without prefix).
func (r *Registry) Resolve(shortID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fullKey, ok := r.reverse[shortID]
	if !ok {
		return "", false
	}
	_, after, found := strings.Cut(fullKey, ":")
	if !found {
		return fullKey, true
	}
	return after, true
}

func (r *Registry) generate(prefix, key string) string {
	h := sha256.Sum256([]byte(key))
	hexStr := hex.EncodeToString(h[:])

	for length := 2; length <= len(hexStr); length++ {
		candidate := prefix + ":" + hexStr[:length]
		if _, collision := r.reverse[candidate]; !collision {
			return candidate
		}
	}
	return prefix + ":" + hexStr
}
