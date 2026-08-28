// Package live holds the in-process waiter registry for live pull.
//
// The registry carries a wakeup signal per user_id, not a queue of envelopes.
// After Notify, the waiting handler queries storage on its own connection.
// Putting payloads on the channel would force the registry to order, dedupe,
// and buffer a copy for every listener.
package live

import "sync"

// Registry holds waiters grouped by user_id. One writer wakes only that
// user's devices; a process serving many users must not broadcast globally.
type Registry struct {
	mu sync.Mutex
	// waiters maps user_id to the set of live pulls currently waiting.
	waiters map[string]map[*Waiter]struct{}
}

// Waiter is one live pull for one user. ch has capacity 1 so a Notify
// that arrives before the handler reaches select is not lost, and a
// second Notify does not block the writer.
type Waiter struct {
	userID string
	ch     chan struct{} // capacity 1: "a signal is already pending"
}

// New returns an empty registry. The HTTP server holds one instance and
// shares it between push (Notify) and pull (Subscribe).
func New() *Registry {
	return &Registry{waiters: make(map[string]map[*Waiter]struct{})}
}

// Subscribe registers a waiter for userID. The channel is created with
// capacity 1 so Notify before the handler blocks on receive is still delivered.
func (r *Registry) Subscribe(userID string) *Waiter {
	w := &Waiter{
		userID: userID,
		ch:     make(chan struct{}, 1),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.waiters[userID]
	if !ok {
		set = make(map[*Waiter]struct{})
		r.waiters[userID] = set
	}
	set[w] = struct{}{}
	return w
}

// Unsubscribe removes w from the registry. It is idempotent: a second call,
// or a call after the user bucket is already empty, does not panic. An empty
// user bucket is deleted so Len and memory stay honest after disconnects.
func (r *Registry) Unsubscribe(w *Waiter) {
	if w == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.waiters[w.userID]
	if !ok {
		return
	}
	delete(set, w)
	if len(set) == 0 {
		delete(r.waiters, w.userID)
	}
}

// Notify wakes every waiter for userID. The send never blocks: a full
// capacity-1 channel already means "wake up", and a second signal is redundant.
// Blocking here would stall the write path behind the slowest listener.
func (r *Registry) Notify(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for w := range r.waiters[userID] {
		select {
		case w.ch <- struct{}{}:
		default:
		}
	}
}

// Len is the number of Waiter values, not the number of distinct user_id keys.
// Tests and the operations panel (step 07) use it to see who is waiting.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, set := range r.waiters {
		n += len(set)
	}
	return n
}

// C is the receive-only wakeup channel. Handlers and tests read from here;
// only Notify writes.
func (w *Waiter) C() <-chan struct{} {
	return w.ch
}
