package earshot

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

const DefaultQueue = 4

var errQueueFull = errors.New("transcription queue full")

type queue struct {
	mu      sync.Mutex
	pending list.List
	limit   int
	running bool
}

type ticket struct {
	ready chan struct{}
	entry *list.Element // nil once the ticket is running; guarded by queue.mu
}

// admit reserves capacity before the request reads its upload. Admitted
// requests can read concurrently and take their turns in admission order.
func (q *queue) admit(ctx context.Context) (*ticket, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.running && q.pending.Len() >= q.limit {
		return nil, errQueueFull
	}
	ticket := &ticket{ready: make(chan struct{})}
	if q.running {
		ticket.entry = q.pending.PushBack(ticket)
	} else {
		q.running = true
		close(ticket.ready)
	}
	return ticket, nil
}

func (t *ticket) wait(ctx context.Context) error {
	select {
	case <-t.ready:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *queue) release(t *ticket) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t.entry != nil {
		q.pending.Remove(t.entry)
		return
	}
	next := q.pending.Front()
	if next == nil {
		q.running = false
		return
	}
	wait := q.pending.Remove(next).(*ticket)
	wait.entry = nil
	close(wait.ready)
}

func (q *queue) state() (running bool, depth int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running, q.pending.Len()
}
