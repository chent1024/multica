package localworkbench

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type localHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
	closed  bool
}

func newLocalHub() *localHub { return &localHub{clients: map[*websocket.Conn]struct{}{}} }

func (h *localHub) add(connection *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		_ = connection.Close()
		return
	}
	h.clients[connection] = struct{}{}
}

func (h *localHub) remove(connection *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, connection)
	h.mu.Unlock()
	_ = connection.Close()
}

func (h *localHub) broadcast(kind string, payload any) {
	raw, _ := json.Marshal(map[string]any{
		"type": kind, "payload": payload,
		"actor_id": LocalActorID, "actor_type": "system",
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	for connection := range h.clients {
		_ = connection.WriteMessage(websocket.TextMessage, raw)
	}
}

func (h *localHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for connection := range h.clients {
		_ = connection.Close()
	}
	h.clients = map[*websocket.Conn]struct{}{}
}
