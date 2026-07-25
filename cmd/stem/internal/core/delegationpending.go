package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type pendingConfirmationStatus int

const (
	pendingStatusOpen pendingConfirmationStatus = iota
	pendingStatusApproved
	pendingStatusDenied
	pendingStatusConsumed
)

// PendingConfirmation records a delegated invocation that requires explicit Botanist
// approval because it crossed a grant's confirm-above impact threshold.
type PendingConfirmation struct {
	ID             string
	Pollen         string
	OperationClass string
	Substrate      string
	Impact         string
	Grant          DelegationGrant // snapshot at creation time
	CreatedAt      time.Time
	ExpiresAt      time.Time
	status         pendingConfirmationStatus
}

// PendingConfirmationStore is an in-memory, process-local store for pending confirmations.
type PendingConfirmationStore struct {
	mu      sync.Mutex
	records map[string]*PendingConfirmation
	now     func() time.Time
}

// NewPendingConfirmationStore creates a new store for pending confirmations.
func NewPendingConfirmationStore() *PendingConfirmationStore {
	return &PendingConfirmationStore{
		records: make(map[string]*PendingConfirmation),
		now:     time.Now,
	}
}

// randomTokenID generates a 16-byte cryptographically secure hex string.
func randomTokenID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create stores a new pending confirmation record.
func (s *PendingConfirmationStore) Create(pollen, operationClass, substrate, impact string, grant DelegationGrant, ttl time.Duration) *PendingConfirmation {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	record := &PendingConfirmation{
		ID:             randomTokenID(),
		Pollen:         pollen,
		OperationClass: operationClass,
		Substrate:      substrate,
		Impact:         impact,
		Grant:          grant.clone(), // Snapshot the grant
		CreatedAt:      now,
		ExpiresAt:      now.Add(ttl),
		status:         pendingStatusOpen,
	}
	s.records[record.ID] = record
	return record
}

// Get retrieves a pending confirmation by ID. Filtered out if expired.
func (s *PendingConfirmationStore) Get(id string) (*PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok || s.now().After(record.ExpiresAt) {
		return nil, false
	}
	// Return a copy to prevent data races on read/write outside the lock.
	// (though returning a pointer is fine if the caller doesn't mutate it,
	// returning a clone is safer if status could be read concurrently).
	// We'll just return a copy of the struct as a pointer.
	cloned := *record
	return &cloned, true
}

// List returns all unexpired, open pending confirmations.
func (s *PendingConfirmationStore) List() []*PendingConfirmation {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	var open []*PendingConfirmation
	for _, record := range s.records {
		if record.status == pendingStatusOpen && !now.After(record.ExpiresAt) {
			cloned := *record
			open = append(open, &cloned)
		}
	}
	return open
}

// Approve marks an open, unexpired pending confirmation as approved.
func (s *PendingConfirmationStore) Approve(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return errors.New("pending confirmation not found")
	}
	if s.now().After(record.ExpiresAt) {
		return errors.New("pending confirmation expired")
	}
	if record.status != pendingStatusOpen {
		return errors.New("pending confirmation is not open")
	}

	record.status = pendingStatusApproved
	return nil
}

// Deny marks an open, unexpired pending confirmation as denied.
func (s *PendingConfirmationStore) Deny(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return errors.New("pending confirmation not found")
	}
	if s.now().After(record.ExpiresAt) {
		return errors.New("pending confirmation expired")
	}
	if record.status != pendingStatusOpen {
		return errors.New("pending confirmation is not open")
	}

	record.status = pendingStatusDenied
	return nil
}

// findApproved returns an unexpired, approved, not-yet-consumed record matching
// (pollen, operationClass, substrate) exactly, or nil. It consumes the record atomically.
func (s *PendingConfirmationStore) findApprovedAndConsume(pollen, operationClass, substrate string) *PendingConfirmation {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for _, record := range s.records {
		if record.status == pendingStatusApproved &&
			!now.After(record.ExpiresAt) &&
			record.Pollen == pollen &&
			record.OperationClass == operationClass &&
			record.Substrate == substrate {

			record.status = pendingStatusConsumed
			cloned := *record
			return &cloned
		}
	}
	return nil
}
