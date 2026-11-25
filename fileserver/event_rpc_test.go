package main

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestPublishEventsRPC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- searpc.Serve(ctx, listener, seafserver.RPCService, seafserver.EventRPCHandlers()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	original := rpcclient
	rpcclient = searpc.Init(path, seafserver.RPCService, 0)
	t.Cleanup(func() { rpcclient = original })

	started := time.Now().Unix()
	publishUpdateEvent("repo", "commit")
	publishRepoEvent(&repoEventData{
		eType: "repo-download-sync", user: "member@example.test",
		ip: "192.0.2.9", repoID: "repo", clientName: "client",
	})

	for _, want := range []map[string]string{
		{"msg_type": "repo-update", "repo_id": "repo", "commit_id": "commit"},
		{"msg_type": "repo-download-sync", "repo_id": "repo", "user_name": "member@example.test",
			"ip": "192.0.2.9", "user_agent": "client", "file_path": "/"},
	} {
		response, err := rpcclient.Call("pop_event", seafileServerChannelEvent)
		if err != nil {
			t.Fatal(err)
		}
		event, ok := response.(map[string]any)
		if !ok || len(event) != 2 {
			t.Fatalf("pop_event = %#v, want content and ctime", response)
		}
		ctime, ok := event["ctime"].(float64)
		if !ok || ctime != float64(int64(ctime)) || ctime < float64(started) || ctime > float64(time.Now().Unix()) {
			t.Fatalf("pop_event ctime = %v, want whole Unix seconds during the test", event["ctime"])
		}
		content, ok := event["content"].(string)
		if !ok {
			t.Fatalf("pop_event content = %#v, want JSON text", event["content"])
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got, want) {
			t.Fatalf("pop_event content = %#v, want %#v", got, want)
		}
	}

	publishUpdateEvent("repo", "next")
	const readers = 8
	start := make(chan struct{})
	type result struct {
		response any
		err      error
	}
	results := make(chan result, readers)
	for range readers {
		go func() {
			<-start
			response, err := rpcclient.Call("pop_event", seafileServerChannelEvent)
			results <- result{response, err}
		}()
	}
	close(start)
	consumed := 0
	for range readers {
		result := <-results
		if result.err != nil {
			t.Error(result.err)
		} else if result.response != nil {
			consumed++
		}
	}
	if consumed != 1 {
		t.Errorf("%d readers consumed the event, want 1", consumed)
	}
}
