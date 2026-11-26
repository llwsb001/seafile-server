package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (w *fileWriter) publishTransfer(ctx context.Context, task *fileMoveTask, write func(*sql.Conn) (bool, error)) error {
	locked := false
	defer func() {
		if locked {
			task.mu.Unlock()
		}
	}()
	_, err := withSQLiteWrite(ctx, w.db, func(conn *sql.Conn) (bool, error) {
		// Once SQLite grants the write lock, serialize cancellation with publication.
		if task != nil {
			task.mu.Lock()
			locked = true
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return write(conn)
	})
	if err == nil && locked {
		task.finished = true
	}
	return err
}

const fileMoveWorkers = 5

type fileMoveQueue struct {
	ctx  context.Context
	stop context.CancelFunc
	work chan struct{}
	jobs sync.WaitGroup
	mu   sync.Mutex
	open map[string]*fileMoveTask
}

type fileMoveTask struct {
	mu          sync.Mutex
	stop        context.CancelFunc
	done, total int64
	finished    bool
	canceled    bool
	failure     string
}

func newFileMoveQueue(ctx context.Context) *fileMoveQueue {
	ctx, stop := context.WithCancel(ctx)
	return &fileMoveQueue{ctx: ctx, stop: stop, work: make(chan struct{}, fileMoveWorkers), open: make(map[string]*fileMoveTask)}
}

func (q *fileMoveQueue) close() {
	q.stop()
	q.jobs.Wait()
}

func (q *fileMoveQueue) submit(report bool, run func(context.Context, *fileMoveTask) *searpc.RPCError) (any, *searpc.RPCError) {
	ctx, stop := context.WithCancel(q.ctx)
	var id string
	var taskID any
	task := &fileMoveTask{stop: stop}
	if report {
		identifier, err := uuid.NewRandom()
		if err != nil {
			stop()
			return nil, fileWriteFailure("generate copy or move task ID", err)
		}
		id = identifier.String()
		taskID = id
		q.mu.Lock()
		q.open[id] = task
		q.mu.Unlock()
	}
	q.jobs.Add(1)
	go func() {
		defer stop()
		defer q.jobs.Done()
		if id != "" {
			defer q.expire(id)
		}
		select {
		case q.work <- struct{}{}:
			defer func() { <-q.work }()
		case <-ctx.Done():
			return
		}
		failure := run(ctx, task)
		task.finish(failure)
	}()
	return map[string]any{"background": true, "task-id": taskID}, nil
}

func (q *fileMoveQueue) expire(taskID string) {
	time.AfterFunc(time.Hour, func() {
		q.mu.Lock()
		delete(q.open, taskID)
		q.mu.Unlock()
	})
}

func (task *fileMoveTask) finish(failure *searpc.RPCError) {
	task.mu.Lock()
	defer task.mu.Unlock()
	task.finished = true
	if failure != nil && !task.canceled {
		task.failure = failure.Message
	}
}

func (task *fileMoveTask) setTotal(total int64) {
	task.mu.Lock()
	defer task.mu.Unlock()
	task.total, task.done = total, 0
}

func (task *fileMoveTask) completeFile() {
	task.mu.Lock()
	defer task.mu.Unlock()
	task.done++
}

func (q *fileMoveQueue) get(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.open[*id]
	if task == nil {
		return nil, nil
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	var reason any
	if task.failure != "" {
		reason = task.failure
	}
	if task.finished || task.canceled {
		delete(q.open, *id)
	}
	return map[string]any{
		"done": task.done, "total": task.total, "canceled": task.canceled, "failed": task.failure != "",
		"failed-reason": reason, "successful": task.finished && !task.canceled && task.failure == "",
	}, nil
}

func (q *fileMoveQueue) cancel(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return 0, nil
	}
	q.mu.Lock()
	task := q.open[*id]
	q.mu.Unlock()
	if task == nil {
		return 0, nil
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.finished || task.canceled {
		return -1, nil
	}
	task.canceled = true
	task.stop()
	return 0, nil
}
