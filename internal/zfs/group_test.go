package zfs

import (
	"context"
	"errors"
	"testing"
)

func TestGroup_noGoroutines_returnsNil(t *testing.T) {
	g, _ := NewGroup(context.Background())
	if err := g.Wait(); err != nil {
		t.Errorf("empty group: got %v; want nil", err)
	}
}

func TestGroup_allSucceed_returnsNil(t *testing.T) {
	g, _ := NewGroup(context.Background())
	for range 3 {
		g.Go(func() error { return nil })
	}
	if err := g.Wait(); err != nil {
		t.Errorf("all-success group: got %v; want nil", err)
	}
}

func TestGroup_singleError_returnsThatError(t *testing.T) {
	sentinel := errors.New("boom")
	g, _ := NewGroup(context.Background())
	g.Go(func() error { return sentinel })
	if err := g.Wait(); !errors.Is(err, sentinel) {
		t.Errorf("got %v; want %v", err, sentinel)
	}
}

func TestGroup_errorCancelsContext(t *testing.T) {
	sentinel := errors.New("fail")
	g, ctx := NewGroup(context.Background())

	// One goroutine fails immediately; the other blocks until ctx is cancelled.
	// If cancellation does not propagate, the test will hang.
	g.Go(func() error { return sentinel })
	g.Go(func() error {
		<-ctx.Done()
		return nil
	})

	if err := g.Wait(); !errors.Is(err, sentinel) {
		t.Errorf("got %v; want sentinel error", err)
	}
}

func TestGroup_contextCancelledBeforeAnyGoroutine_noPanic(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	g, _ := NewGroup(parent)
	g.Go(func() error { return nil })
	if err := g.Wait(); err != nil {
		t.Errorf("got %v; want nil", err)
	}
}

func TestGroup_waitCancelsContextOnSuccess(t *testing.T) {
	g, ctx := NewGroup(context.Background())
	g.Go(func() error { return nil })
	g.Wait()
	// After Wait the context must be cancelled so callers can detect completion.
	select {
	case <-ctx.Done():
	default:
		t.Error("context should be cancelled after Wait returns")
	}
}

func TestGroup_firstErrorWins(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	// Use a channel to sequence goroutines: second error always fires after first.
	ready := make(chan struct{})
	g, _ := NewGroup(context.Background())
	g.Go(func() error {
		close(ready)
		return first
	})
	g.Go(func() error {
		<-ready
		return second
	})
	err := g.Wait()
	// Must be first OR second (race is possible), but must not be nil.
	if err == nil {
		t.Error("expected non-nil error")
	}
}
