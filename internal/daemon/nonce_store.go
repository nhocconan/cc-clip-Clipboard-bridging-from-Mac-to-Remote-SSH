package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shunmei/cc-clip/internal/token"
)

const nonceStoreFile = "notify-nonces.json"

type persistedNonceStore struct {
	Version int              `json:"version"`
	Nonces  []persistedNonce `json:"nonces"`
}

type persistedNonce struct {
	Nonce     string    `json:"nonce"`
	Host      string    `json:"host,omitempty"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func nonceStorePath() (string, error) {
	dir, err := token.TokenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, nonceStoreFile), nil
}

// LoadPersistedNonces restores the notification nonce registry from disk.
// Expired entries are skipped so a stale store cannot resurrect old credentials.
func (s *Server) LoadPersistedNonces() (int, error) {
	path, err := nonceStorePath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read nonce store: %w", err)
	}

	var store persistedNonceStore
	if err := json.Unmarshal(data, &store); err != nil {
		return 0, fmt.Errorf("decode nonce store: %w", err)
	}

	now := time.Now()
	next := make(map[string]nonceEntry)
	order := make([]string, 0, len(store.Nonces))
	for _, item := range store.Nonces {
		if item.Nonce == "" || !now.Before(item.ExpiresAt) {
			continue
		}
		if _, exists := next[item.Nonce]; exists {
			continue
		}
		next[item.Nonce] = nonceEntry{
			Host:      item.Host,
			IssuedAt:  item.IssuedAt,
			ExpiresAt: item.ExpiresAt,
		}
		order = append(order, item.Nonce)
	}
	for len(next) > maxNonces && len(order) > 0 {
		oldest := order[0]
		order = order[1:]
		delete(next, oldest)
	}

	s.noncesMu.Lock()
	s.notifyNonces = next
	s.notifyNoncesOrder = order
	s.noncesMu.Unlock()

	return len(next), nil
}

// persistNoncesLocked writes the current nonce registry to disk atomically.
// Caller must hold s.noncesMu.
func (s *Server) persistNoncesLocked() error {
	path, err := nonceStorePath()
	if err != nil {
		return err
	}
	store := persistedNonceStore{
		Version: 1,
		Nonces:  make([]persistedNonce, 0, len(s.notifyNonces)),
	}
	seen := make(map[string]struct{}, len(s.notifyNonces))
	for _, nonce := range s.notifyNoncesOrder {
		entry, ok := s.notifyNonces[nonce]
		if !ok {
			continue
		}
		store.Nonces = append(store.Nonces, persistedNonce{
			Nonce:     nonce,
			Host:      entry.Host,
			IssuedAt:  entry.IssuedAt,
			ExpiresAt: entry.ExpiresAt,
		})
		seen[nonce] = struct{}{}
	}
	for nonce, entry := range s.notifyNonces {
		if _, ok := seen[nonce]; ok {
			continue
		}
		store.Nonces = append(store.Nonces, persistedNonce{
			Nonce:     nonce,
			Host:      entry.Host,
			IssuedAt:  entry.IssuedAt,
			ExpiresAt: entry.ExpiresAt,
		})
	}

	data, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("encode nonce store: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic replaces path with data, mode 0600, through a synced temp file
// in the same directory and a rename, so a crash leaves either the old file or
// the new one, never a torn write.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := replaceStateFile(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s into place: %w", path, err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func cloneNonceMap(src map[string]nonceEntry) map[string]nonceEntry {
	dst := make(map[string]nonceEntry, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
