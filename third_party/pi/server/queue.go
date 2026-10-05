package server

import (
	"context"
	"sync"
)

// serialQueue runs submitted jobs one at a time, in submission order, on a
// goroutine it spawns on demand and lets go of when the queue drains.
//
// DIVERGENCE (deliberate): pi serialises this kind of work by chaining onto a
// promise it keeps in a field — `queue = queue.then(next)`. That both orders
// the jobs and lets each caller await its own. Go has no promise to chain, and
// every caller here is fire-and-forget, so the ordering is what matters. A
// queue plus one drain goroutine reproduces it, and Wait gives shutdown the
// join point a promise chain would not have needed.
//
// Wait is deliberately not a sync.WaitGroup: submissions keep arriving from
// other goroutines while shutdown waits, and Add racing an already-zero Wait is
// exactly the misuse a WaitGroup forbids.
//
// A job must not call Wait on the queue that is running it.
type serialQueue struct {
	mu      sync.Mutex
	jobs    []func()
	running bool
	// idle is closed the next time the queue drains, and a fresh one is made
	// when somebody next waits. It is a channel rather than a sync.Cond
	// because a waiter has to be able to give up when its context expires.
	idle chan struct{}
}

// Go submits a job. It never blocks.
func (q *serialQueue) Go(job func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	if q.running {
		return
	}
	q.running = true
	go q.drain()
}

func (q *serialQueue) drain() {
	for {
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.running = false
			if q.idle != nil {
				close(q.idle)
				q.idle = nil
			}
			q.mu.Unlock()
			return
		}
		job := q.jobs[0]
		// Clearing the slot before resliceing matters: the backing array
		// survives the reslice, so a queue that has run for a while would
		// otherwise still be holding every closure it has already executed,
		// and with them whatever those closures captured.
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		job()
	}
}

// Wait blocks until the queue has drained. Jobs submitted while it waits are
// still run before it returns.
func (q *serialQueue) Wait() { _ = q.WaitContext(context.Background()) }

// WaitContext is Wait, given up on when ctx is done — it returns ctx.Err()
// then. The queue keeps running: the caller has stopped waiting for the work,
// which is not the same as the work stopping.
func (q *serialQueue) WaitContext(ctx context.Context) error {
	for {
		q.mu.Lock()
		if !q.running {
			q.mu.Unlock()
			return nil
		}
		if q.idle == nil {
			q.idle = make(chan struct{})
		}
		idle := q.idle
		q.mu.Unlock()

		select {
		case <-idle:
			// Round again: a job submitted just as the queue drained starts a
			// new run, and Wait covers that one too.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
