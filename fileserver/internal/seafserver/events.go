package seafserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type event struct {
	Content string `json:"content"`
	CTime   int64  `json:"ctime"`
}

// EventRPCHandlers exposes a new process-local queue shared by the event RPCs.
func EventRPCHandlers() map[string]searpc.Handler {
	return newEventQueue().rpcHandlers()
}

// EventRPCHandlers exposes the reader's local event queue to the RPC server.
func (r *Reader) EventRPCHandlers() map[string]searpc.Handler {
	return r.events.rpcHandlers()
}

func newEventQueue() *eventQueue {
	return &eventQueue{channels: make(map[string][]event)}
}

func (q *eventQueue) rpcHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"publish_event": q.publish,
		"pop_event":     q.pop,
	}
}

type eventQueue struct {
	mu       sync.Mutex
	channels map[string][]event
}

func (q *eventQueue) publish(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	channel, content := stringArgument(args, 0), stringArgument(args, 1)
	if channel == nil || content == nil {
		return -1, rpcError(errBadArgs, "Argument should not be null")
	}
	q.enqueue(*channel, *content)
	return 0, nil
}

func (q *eventQueue) enqueue(channel, content string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.channels[channel] = append(q.channels[channel], event{Content: content, CTime: time.Now().Unix()})
}

// PublishRepoUpdate delivers the notifications for an already committed branch.
func (r *Reader) PublishRepoUpdate(ctx context.Context, repoID, commitID string, virtual bool) {
	if err := NotifyRepoUpdate(context.WithoutCancel(ctx), repoID, commitID); err != nil {
		log.Printf("notify repo update %s: %v", repoID, err)
	}
	if !virtual {
		content := fmt.Sprintf(`{"msg_type": "repo-update", "repo_id": %q, "commit_id": %q}`, repoID, commitID)
		r.events.enqueue("seaf_server.event", content)
	}
}

func (q *eventQueue) pop(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	channel := stringArgument(args, 0)
	if channel == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.channels[*channel]
	if len(pending) == 0 {
		return nil, nil
	}
	first := pending[0]
	if len(pending) == 1 {
		delete(q.channels, *channel)
	} else {
		pending[0] = event{}
		q.channels[*channel] = pending[1:]
	}
	return first, nil
}
