package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

type Job struct {
	Name     string
	Payload  []byte
	Attempts int
}

type Handler func(ctx context.Context, job Job) error

type Queue interface {
	Enqueue(ctx context.Context, name string, payload any) error
	Register(name string, handler Handler)
	Start(ctx context.Context)
	Stop()
}

type InProcess struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	ch       chan Job
	log      *slog.Logger
	wg       sync.WaitGroup
	stop     chan struct{}
	once     sync.Once
}

func NewInProcess(buffer int, log *slog.Logger) *InProcess {
	if buffer <= 0 {
		buffer = 256
	}
	return &InProcess{
		handlers: make(map[string]Handler),
		ch:       make(chan Job, buffer),
		log:      log,
		stop:     make(chan struct{}),
	}
}

func (q *InProcess) Register(name string, handler Handler) {
	q.mu.Lock()
	q.handlers[name] = handler
	q.mu.Unlock()
}

func (q *InProcess) Enqueue(ctx context.Context, name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	select {
	case q.ch <- Job{Name: name, Payload: b}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-q.stop:
		return context.Canceled
	}
}

func (q *InProcess) Start(ctx context.Context) {
	for i := 0; i < 4; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
}

func (q *InProcess) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case job := <-q.ch:
			q.run(ctx, job)
		case <-ctx.Done():
			return
		case <-q.stop:
			return
		}
	}
}

func (q *InProcess) run(ctx context.Context, job Job) {
	q.mu.RLock()
	h := q.handlers[job.Name]
	q.mu.RUnlock()
	if h == nil {
		q.log.Warn("no queue handler", "job", job.Name)
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := h(ctx, job); err != nil {
			q.log.Error("job failed", "job", job.Name, "attempt", attempt+1, "error", err)
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			continue
		}
		return
	}
}

func (q *InProcess) Stop() {
	q.once.Do(func() { close(q.stop) })
	q.wg.Wait()
}
