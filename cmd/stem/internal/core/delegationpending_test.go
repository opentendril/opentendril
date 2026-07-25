package core

import (
	"sync"
	"testing"
	"time"
)

func TestPendingConfirmationStore(t *testing.T) {
	t.Run("Create and Get", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		grant := DelegationGrant{Pollen: "test-pollen"}
		record := s.Create("test-pollen", "op1", "sub1", "high", grant, time.Hour)

		if record.ID == "" {
			t.Error("expected non-empty ID")
		}

		fetched, ok := s.Get(record.ID)
		if !ok {
			t.Fatal("expected to find record")
		}
		if fetched.ID != record.ID {
			t.Errorf("got %q, want %q", fetched.ID, record.ID)
		}
		if fetched.Grant.Pollen != grant.Pollen {
			t.Errorf("got %q, want %q", fetched.Grant.Pollen, grant.Pollen)
		}
	})

	t.Run("Approve and Deny", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		r1 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour)
		r2 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour)

		if err := s.Approve(r1.ID); err != nil {
			t.Errorf("unexpected error on approve: %v", err)
		}
		if err := s.Deny(r2.ID); err != nil {
			t.Errorf("unexpected error on deny: %v", err)
		}

		if fetched, _ := s.Get(r1.ID); fetched.status != pendingStatusApproved {
			t.Errorf("expected approved status, got %v", fetched.status)
		}
		if fetched, _ := s.Get(r2.ID); fetched.status != pendingStatusDenied {
			t.Errorf("expected denied status, got %v", fetched.status)
		}
	})

	t.Run("findApprovedAndConsume", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		s.Create("p1", "o1", "s1", "high", DelegationGrant{}, time.Hour) // open
		r2 := s.Create("p2", "o2", "s2", "high", DelegationGrant{}, time.Hour)
		_ = s.Approve(r2.ID)

		// Doesn't match open record
		if s.findApprovedAndConsume("p1", "o1", "s1") != nil {
			t.Error("expected nil for open record")
		}

		// Doesn't match wrong pollen
		if s.findApprovedAndConsume("p3", "o2", "s2") != nil {
			t.Error("expected nil for wrong pollen")
		}

		// Matches approved
		approved := s.findApprovedAndConsume("p2", "o2", "s2")
		if approved == nil {
			t.Fatal("expected to find approved record")
		}
		if approved.ID != r2.ID {
			t.Errorf("got %q, want %q", approved.ID, r2.ID)
		}

		// Consumed, should not be found again
		if s.findApprovedAndConsume("p2", "o2", "s2") != nil {
			t.Error("expected nil after consume")
		}
	})

	t.Run("Expiry", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		now := time.Now()
		s.now = func() time.Time { return now }

		r1 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour)

		// Advance time past expiry
		now = now.Add(2 * time.Hour)

		if _, ok := s.Get(r1.ID); ok {
			t.Error("expected expired record not to be returned")
		}
		if err := s.Approve(r1.ID); err == nil {
			t.Error("expected error approving expired record")
		}

		// Create an approved record and check findApprovedAndConsume ignores expired
		now = time.Now()
		r2 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour)
		_ = s.Approve(r2.ID)
		now = now.Add(2 * time.Hour)
		if s.findApprovedAndConsume("p", "o", "s") != nil {
			t.Error("expected nil for expired approved record")
		}
	})

	t.Run("List", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		now := time.Now()
		s.now = func() time.Time { return now }

		r1 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour) // open
		r2 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour) // approved
		_ = s.Approve(r2.ID)
		r3 := s.Create("p", "o", "s", "high", DelegationGrant{}, time.Hour) // open but will expire

		now = now.Add(time.Minute)
		// expire r3
		s.records[r3.ID].ExpiresAt = now.Add(-time.Minute)

		list := s.List()
		if len(list) != 1 {
			t.Fatalf("expected 1 record in list, got %d", len(list))
		}
		if list[0].ID != r1.ID {
			t.Errorf("got %q, want %q", list[0].ID, r1.ID)
		}
	})

	t.Run("Concurrency", func(t *testing.T) {
		s := NewPendingConfirmationStore()
		grant := DelegationGrant{Pollen: "p"}
		var wg sync.WaitGroup

		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				r := s.Create("p", "o", "s", "high", grant, time.Hour)
				if idx%2 == 0 {
					_ = s.Approve(r.ID)
				}
				_ = s.findApprovedAndConsume("p", "o", "s")
				s.List()
			}(i)
		}
		wg.Wait()
	})
}
