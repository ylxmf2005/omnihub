package query

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

const defaultQuerySessionTTL = 15 * time.Minute

var ErrContinuation = errors.New("invalid or expired continuation")

// SessionStore 保存一次搜索尚未交付的 merge buffer 和各来源私有 cursor。
// token 只在当前 serve 进程与 TTL 内有效，并且每一枚只能成功消费一次。
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*querySession
	now      func() time.Time
	ttl      time.Duration
}

type querySession struct {
	fingerprint [32]byte
	expiresAt   time.Time
	inUse       bool
	operation   core.Operation

	selectedIDs []string
	executions  map[string]core.Execution
	coverage    map[string]core.Coverage
	errors      []core.Error
	buffer      []routedItem
	cursors     map[string]string
	seen        map[string]struct{}
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*querySession), now: time.Now, ttl: defaultQuerySessionTTL}
}

func (store *SessionStore) acquire(token string, operation core.Operation) (*querySession, error) {
	if store == nil {
		return nil, ErrContinuation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked()
	session, ok := store.sessions[token]
	if !ok || session.inUse || session.fingerprint != continuationFingerprint(operation) {
		return nil, ErrContinuation
	}
	session.inUse = true
	return session, nil
}

func (store *SessionStore) rotate(oldToken string, session *querySession, keep bool) (*string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.sessions[oldToken]
	if !ok || current != session || !session.inUse {
		return nil, ErrContinuation
	}
	delete(store.sessions, oldToken)
	if !keep {
		return nil, nil
	}
	token, err := newContinuationToken()
	if err != nil {
		return nil, err
	}
	session.inUse = false
	session.expiresAt = store.now().UTC().Add(store.sessionTTL())
	store.sessions[token] = session
	return &token, nil
}

func (store *SessionStore) release(token string, session *querySession) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.sessions[token]; ok && current == session {
		session.inUse = false
	}
}

func (store *SessionStore) create(operation core.Operation, session *querySession) (*string, error) {
	if store == nil || session == nil {
		return nil, nil
	}
	token, err := newContinuationToken()
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked()
	session.fingerprint = continuationFingerprint(operation)
	session.expiresAt = store.now().UTC().Add(store.sessionTTL())
	store.sessions[token] = session
	return &token, nil
}

func (store *SessionStore) sessionTTL() time.Duration {
	if store.ttl > 0 {
		return store.ttl
	}
	return defaultQuerySessionTTL
}

func (store *SessionStore) pruneLocked() {
	now := store.now().UTC()
	for token, session := range store.sessions {
		if !session.inUse && !session.expiresAt.After(now) {
			delete(store.sessions, token)
		}
	}
}

func continuationFingerprint(operation core.Operation) [32]byte {
	copy := operation
	copy.Continuation = nil
	copy.Limit = 0
	copy.DeadlineMS = 0
	encoded, _ := json.Marshal(copy)
	return sha256.Sum256(encoded)
}

func newContinuationToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "ctn_" + hex.EncodeToString(value), nil
}
