package searpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestServeConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})
	if err := clientConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const testService = "test-service"
	go serveConnection(context.Background(), serverConn, testService, map[string]Handler{
		"integer": func(_ context.Context, args []json.RawMessage) (any, *RPCError) {
			return args[0], nil
		},
		"invalid": func(context.Context, []json.RawMessage) (any, *RPCError) {
			return nil, &RPCError{Code: 503, Message: "Invalid repo id"}
		},
	})

	for _, test := range []struct {
		call string
		want string
	}{
		{`["integer",9007199254740993]`, `{"ret":9007199254740993}`},
		{`["invalid"]`, `{"ret":null,"err_code":503,"err_msg":"Invalid repo id"}`},
		{`["absent"]`, `{"err_code":500,"err_msg":"cannot find function absent."}`},
	} {
		payload, err := json.Marshal(request{Service: testService, Request: test.call})
		if err != nil {
			t.Fatal(err)
		}
		if err := binary.Write(clientConn, binary.LittleEndian, uint32(len(payload))); err != nil {
			t.Fatal(err)
		}
		if _, err := clientConn.Write(payload); err != nil {
			t.Fatal(err)
		}
		var size uint32
		if err := binary.Read(clientConn, binary.LittleEndian, &size); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(clientConn, body); err != nil {
			t.Fatal(err)
		}
		if string(body) != test.want {
			t.Fatalf("response for %s: got %s, want %s", test.call, body, test.want)
		}
	}
}
