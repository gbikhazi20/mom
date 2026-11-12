package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeDaemon serves the control-socket protocol on a Unix socket: a
// WebSocket upgrade, then JSON-RPC, with a notification sent before every
// response to check the client skips it.
func fakeDaemon(t *testing.T, handle func(method string, params json.RawMessage) any) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mom-rpc-") // short path: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var req struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			json.Unmarshal(data, &req)
			if req.ID == nil {
				continue // the initialized notification
			}
			note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "thread/status/changed", "params": map[string]any{}})
			c.Write(ctx, websocket.MessageText, note)
			resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID}
			switch v := handle(req.Method, req.Params).(type) {
			case *rpcError:
				resp["error"] = v
			default:
				resp["result"] = v
			}
			b, _ := json.Marshal(resp)
			c.Write(ctx, websocket.MessageText, b)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestListThreadsPagesAndSkipsNotifications(t *testing.T) {
	var gotKinds []string
	sock := fakeDaemon(t, func(method string, params json.RawMessage) any {
		switch method {
		case "initialize":
			return map[string]any{"userAgent": "fake"}
		case "thread/list":
			var p struct {
				Cursor      *string  `json:"cursor"`
				SourceKinds []string `json:"sourceKinds"`
			}
			json.Unmarshal(params, &p)
			gotKinds = p.SourceKinds
			if p.Cursor == nil {
				return map[string]any{"data": []map[string]any{{"id": "a", "status": map[string]any{"type": "idle"}}}, "nextCursor": "page2"}
			}
			return map[string]any{"data": []map[string]any{{"id": "b", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}}, "nextCursor": nil}
		case "thread/loaded/list":
			return map[string]any{"data": []string{"b"}}
		}
		return &rpcError{Code: -32601, Message: "no such method"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	threads, err := c.ListThreads(ctx, SourceKinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "a" || threads[1].Status.ActiveFlags[0] != "waitingOnApproval" {
		t.Errorf("threads = %+v", threads)
	}
	if len(gotKinds) != len(SourceKinds) {
		t.Errorf("sourceKinds sent = %v", gotKinds)
	}
	ids, err := c.LoadedThreads(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "b" {
		t.Errorf("loaded = %v, %v", ids, err)
	}
	if _, err := c.ReadThread(ctx, "x"); err == nil {
		t.Error("expected the server's error to come back")
	}
}

func TestDialNotRunning(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, filepath.Join(t.TempDir(), "missing.sock")); !errors.Is(err, ErrNotRunning) {
		t.Errorf("missing socket: %v", err)
	}
	// A stale socket file left behind by a dead daemon.
	dir, _ := os.MkdirTemp("/tmp", "mom-rpc-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := Dial(ctx, sock); !errors.Is(err, ErrNotRunning) {
		t.Errorf("stale socket: %v", err)
	}
}
