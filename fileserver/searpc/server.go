package searpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

const maxRequestSize = 16 << 20

// RPCError carries the application error code and message used by Searpc clients.
type RPCError struct {
	Code    int    `json:"err_code"`
	Message string `json:"err_msg"`
}

// Handler receives the positional arguments of a Searpc function.
type Handler func(context.Context, []json.RawMessage) (any, *RPCError)

// NoReturn omits the ret field, as the C marshaller does for an invalid UTF-8 string.
type NoReturn struct{}

type response struct {
	Return *any `json:"ret,omitempty"`
	*RPCError
}

// Serve handles Searpc calls until the context is canceled or the listener fails.
func Serve(ctx context.Context, listener net.Listener, service string, handlers map[string]Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	var connections sync.WaitGroup
	defer func() {
		cancel()
		connections.Wait()
	}()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			serveConnection(ctx, conn, service, handlers)
		}()
	}
}

func serveConnection(ctx context.Context, conn net.Conn, service string, handlers map[string]Handler) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		var size uint32
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil || size == 0 || size > maxRequestSize {
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var call struct {
			Service *string `json:"service"`
			Request *string `json:"request"`
		}
		if err := json.Unmarshal(body, &call); err != nil || call.Service == nil || call.Request == nil {
			return
		}
		result := dispatch(ctx, service, handlers, *call.Service, *call.Request)
		payload, err := json.Marshal(result)
		if err != nil {
			return
		}
		if err := binary.Write(writer, binary.LittleEndian, uint32(len(payload))); err != nil {
			return
		}
		if _, err := writer.Write(payload); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func dispatch(ctx context.Context, service string, handlers map[string]Handler, requestedService, body string) any {
	if requestedService != service {
		return RPCError{Code: 501, Message: fmt.Sprintf("cannot find service %s.", requestedService)}
	}
	var args []json.RawMessage
	var method string
	if err := json.Unmarshal([]byte(body), &args); err != nil || len(args) == 0 || json.Unmarshal(args[0], &method) != nil {
		return RPCError{Code: 511, Message: "failed to load RPC call"}
	}
	handler, ok := handlers[method]
	if !ok {
		return RPCError{Code: 500, Message: fmt.Sprintf("cannot find function %s.", method)}
	}
	result, rpcErr := handler(ctx, args[1:])
	if _, absent := result.(NoReturn); absent {
		return response{RPCError: rpcErr}
	}
	return response{Return: &result, RPCError: rpcErr}
}
