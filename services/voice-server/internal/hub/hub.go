package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Hub рассылает JSON всем подключённым WebSocket-клиентам (delivery layer).
type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func New() *Hub {
	return &Hub{clients: make(map[*websocket.Conn]struct{})}
}

func (h *Hub) Register(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) Unregister(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	_ = c.Close()
}

func (h *Hub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("hub marshal: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dead := make([]*websocket.Conn, 0)
	for c := range h.clients {
		_ = c.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		delete(h.clients, c)
		_ = c.Close()
	}
}

func (h *Hub) BroadcastStatus(status string) {
	h.BroadcastJSON(map[string]interface{}{
		"type":   "status",
		"status": status,
	})
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	h.Register(c)
	go func() {
		defer h.Unregister(c)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
