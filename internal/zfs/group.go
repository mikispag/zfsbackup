package zfs

import (
	"context"
	"sync"
)

// Group runs a collection of goroutines and collects the first error.
// If any goroutine returns an error the context is cancelled, signalling all
// others to stop. Equivalent to errgroup.Group with WithContext for the
// subset of the errgroup API this project uses.
type Group struct {
	wg     sync.WaitGroup
	once   sync.Once
	err    error
	cancel context.CancelFunc
}

// NewGroup returns a Group and a derived context that is cancelled when the
// first goroutine returns a non-nil error or Wait returns.
func NewGroup(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

// Go runs f in a new goroutine. If f returns a non-nil error the group's
// context is cancelled and the error is recorded (first error wins).
func (g *Group) Go(f func() error) {
	g.wg.Go(func() {
		if err := f(); err != nil {
			g.once.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	})
}

// Wait blocks until all goroutines have returned, then cancels the context
// and returns the first non-nil error (if any).
func (g *Group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
