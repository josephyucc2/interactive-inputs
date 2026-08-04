package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boasihq/interactive-inputs/internal/fields"
)

// Status represents the current state of a session
type Status string

const (
	StatusPending   Status = "pending"
	StatusSubmitted Status = "submitted"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
)

// Session represents a single interactive input form session
type Session struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Fields    *fields.Fields    `json:"fields"`
	Status    Status            `json:"status"`
	Results   map[string]string `json:"results,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`

	// CacheDir is the base cache directory for file uploads in this session
	CacheDir string `json:"-"`

	// InputFieldLabelToCacheDirMapping maps file/multifile field labels to their cache directories
	InputFieldLabelToCacheDirMapping map[string]string `json:"-"`

	done chan struct{}
	mu   sync.Mutex
}

// Manager manages concurrent sessions
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// NewManager creates a new session manager
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateSession creates a new session with the given parameters
func (m *Manager) CreateSession(title string, fieldsData *fields.Fields, timeoutSeconds int) (*Session, error) {
	id := generateID()
	now := time.Now()

	sess := &Session{
		ID:                               id,
		Title:                            title,
		Fields:                           fieldsData,
		Status:                           StatusPending,
		CreatedAt:                        now,
		ExpiresAt:                        now.Add(time.Duration(timeoutSeconds) * time.Second),
		InputFieldLabelToCacheDirMapping: make(map[string]string),
		done:                             make(chan struct{}),
	}

	// Create cache dirs for file/multifile fields
	if fieldsData != nil {
		baseCacheDir := filepath.Join(os.TempDir(), "interactive-inputs-sessions", id, "cache")
		for _, f := range fieldsData.Fields {
			if f.Properties.Type != "file" && f.Properties.Type != "multifile" {
				continue
			}
			if sess.CacheDir == "" {
				if err := os.MkdirAll(baseCacheDir, os.ModePerm); err != nil {
					return nil, fmt.Errorf("unable to create cache dir: %w", err)
				}
				sess.CacheDir = baseCacheDir
			}
			inputFieldCacheDir, err := os.MkdirTemp(baseCacheDir, fmt.Sprintf("%s-", f.Label))
			if err != nil {
				return nil, fmt.Errorf("unable to create field cache dir: %w", err)
			}
			sess.InputFieldLabelToCacheDirMapping[f.Label] = inputFieldCacheDir
		}
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	// Auto-expire goroutine
	go func() {
		timer := time.NewTimer(time.Until(sess.ExpiresAt))
		defer timer.Stop()
		select {
		case <-timer.C:
			sess.mu.Lock()
			if sess.Status == StatusPending {
				sess.Status = StatusExpired
				close(sess.done)
			}
			sess.mu.Unlock()
		case <-sess.done:
			// Already completed
		}
	}()

	return sess, nil
}

// GetSession returns a session by ID
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// SubmitSession marks a session as submitted with the given results
func (m *Manager) SubmitSession(id string, results map[string]string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != StatusPending {
		return fmt.Errorf("session is not pending: %s", s.Status)
	}
	s.Results = results
	s.Status = StatusSubmitted
	close(s.done)
	return nil
}

// CancelSession marks a session as cancelled
func (m *Manager) CancelSession(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != StatusPending {
		return fmt.Errorf("session is not pending: %s", s.Status)
	}
	s.Status = StatusCancelled
	close(s.done)
	return nil
}

// WaitForSession blocks until the session is completed or the context is cancelled
func (m *Manager) WaitForSession(ctx context.Context, id string) (*Session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}

	select {
	case <-s.done:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// StartCleanupRoutine starts a background goroutine that removes expired sessions
func (m *Manager) StartCleanupRoutine(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.cleanupExpired()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) cleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, s := range m.sessions {
		// Remove sessions that finished more than 5 minutes ago
		if s.Status != StatusPending && now.After(s.ExpiresAt.Add(5*time.Minute)) {
			if s.CacheDir != "" {
				os.RemoveAll(s.CacheDir)
			}
			delete(m.sessions, id)
		}
	}
}

// Shutdown cancels all pending sessions and cleans up
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.mu.Lock()
		if s.Status == StatusPending {
			s.Status = StatusCancelled
			close(s.done)
		}
		if s.CacheDir != "" {
			os.RemoveAll(s.CacheDir)
		}
		s.mu.Unlock()
	}
}
