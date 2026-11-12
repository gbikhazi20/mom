// Package codexrpc talks to the Codex app-server daemon over its control
// socket: JSON-RPC 2.0, one message per WebSocket frame, over a Unix socket.
// mom only uses read-only thread methods, so nothing here changes Codex state.
package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
)

// ErrNotRunning means there is no daemon listening on the socket.
var ErrNotRunning = errors.New("the Codex app-server daemon isn't running")

// SocketPath is where the daemon listens for a given CODEX_HOME.
func SocketPath(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

type Client struct {
	conn   *websocket.Conn
	nextID int
}

// Dial connects and completes the initialize handshake.
func Dial(ctx context.Context, socket string) (*Client, error) {
	if _, err := os.Stat(socket); err != nil {
		return nil, ErrNotRunning
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return nil, ErrNotRunning
		}
		return nil, fmt.Errorf("connect to Codex daemon: %w", err)
	}
	conn.SetReadLimit(64 << 20) // a page of threads easily exceeds the 32KB default
	c := &Client{conn: conn}
	init := map[string]any{"clientInfo": map[string]string{"name": "mom", "version": "1"}}
	if err := c.Call(ctx, "initialize", init, nil); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, err
	}
	if err := c.notify(ctx, "initialized"); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error { return c.conn.Close(websocket.StatusNormalClosure, "") }

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("codex rpc error %d: %s", e.Code, e.Message) }

// Call sends a request and waits for its response, skipping any
// notifications the server sends in between.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(ctx, req); err != nil {
		return err
	}
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("%s: bad message: %w", method, err)
		}
		if msg.ID == nil || *msg.ID != id || msg.Method != "" {
			continue // a notification, or a server request we don't handle
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %w", method, msg.Error)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

func (c *Client) notify(ctx context.Context, method string) error {
	return c.write(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
}

func (c *Client) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}

// --- thread methods ---

type Thread struct {
	ID        string   `json:"id"`
	Name      *string  `json:"name"`
	Preview   string   `json:"preview"`
	Cwd       string   `json:"cwd"`
	Status    Status   `json:"status"`
	GitInfo   *GitInfo `json:"gitInfo"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	Source    any      `json:"source"` // a string or a tagged object, depending on kind
	// Originator is the client that created the thread (codex-tui,
	// codex_exec, ...). Unlike Source it tells headless runs apart reliably.
	Originator *string `json:"originator"`
	Ephemeral  bool    `json:"ephemeral"`
	Path       *string `json:"path"`
	Parent     *string `json:"parentThreadId"`
}

type GitInfo struct {
	SHA       *string `json:"sha"`
	Branch    *string `json:"branch"`
	OriginURL *string `json:"originUrl"`
}

type Status struct {
	Type        string   `json:"type"` // notLoaded | idle | systemError | active
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

// SourceKinds mom lists: everything a person or an agent started, but not
// sub-agents, whose parent thread already stands for them.
var SourceKinds = []string{"cli", "vscode", "exec", "appServer", "unknown"}

// ListThreads returns every thread of the given source kinds, newest first.
func (c *Client) ListThreads(ctx context.Context, kinds []string) ([]Thread, error) {
	var all []Thread
	var cursor *string
	for {
		params := map[string]any{
			"limit":          200,
			"sortKey":        "updated_at",
			"useStateDbOnly": true,
			"sourceKinds":    kinds,
		}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		var page struct {
			Data       []Thread `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := c.Call(ctx, "thread/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" || len(page.Data) == 0 {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// LoadedThreads returns the ids of threads the daemon has in memory.
func (c *Client) LoadedThreads(ctx context.Context) ([]string, error) {
	var res struct {
		Data []string `json:"data"`
	}
	err := c.Call(ctx, "thread/loaded/list", map[string]any{}, &res)
	return res.Data, err
}

// ReadThread returns one thread's metadata, without its turns.
func (c *Client) ReadThread(ctx context.Context, id string) (Thread, error) {
	var res struct {
		Thread Thread `json:"thread"`
	}
	err := c.Call(ctx, "thread/read", map[string]any{"threadId": id}, &res)
	return res.Thread, err
}

// DefaultTimeout bounds a whole conversation with the daemon.
const DefaultTimeout = 10 * time.Second
