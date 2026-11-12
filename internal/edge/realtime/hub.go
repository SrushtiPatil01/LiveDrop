package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/core/model"
	"github.com/SrushtiPatil01/LiveDrop/internal/id"
	"github.com/SrushtiPatil01/LiveDrop/internal/metrics"
	redisstore "github.com/SrushtiPatil01/LiveDrop/internal/store/redis"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
	sendBufferSize = 64
)

type Hub struct {
	mu       sync.RWMutex
	rooms    map[string]map[*client]struct{}
	closed   bool
	clients  sync.WaitGroup
	redis    *redisstore.Store
	logger   *slog.Logger
	upgrader websocket.Upgrader
}

type client struct {
	showID string
	conn   *websocket.Conn
	send   chan []byte
}

func NewHub(cache *redisstore.Store, logger *slog.Logger) *Hub {
	return &Hub{
		rooms:  make(map[string]map[*client]struct{}),
		redis:  cache,
		logger: logger,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     sameOrigin,
		},
	}
}

func (h *Hub) Handle(w http.ResponseWriter, r *http.Request, showID string) {
	if showID == "" {
		http.Error(w, "show ID is required", http.StatusBadRequest)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{showID: showID, conn: conn, send: make(chan []byte, sendBufferSize)}
	if !h.add(c) {
		_ = conn.Close()
		return
	}
	defer h.clients.Done()
	count, err := h.redis.CountViewers(r.Context(), showID, 1)
	if err != nil {
		h.logger.Error("could not update viewer count", "error", err, "show_id", showID)
		h.remove(c)
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "temporarily unavailable"),
			time.Now().Add(writeWait))
		_ = conn.Close()
		return
	}
	if err := h.publishViewerCount(r.Context(), showID, count); err != nil {
		h.logger.Error("could not publish viewer count", "error", err, "show_id", showID)
	}
	metrics.WebsocketActiveConnections.Inc()
	defer func() {
		h.remove(c)
		metrics.WebsocketActiveConnections.Dec()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		count, err := h.redis.CountViewers(ctx, showID, -1)
		if err != nil {
			h.logger.Error("could not decrement viewer count", "error", err, "show_id", showID)
			return
		}
		if err := h.publishViewerCount(ctx, showID, count); err != nil {
			h.logger.Error("could not publish viewer count", "error", err, "show_id", showID)
		}
	}()
	go c.writePump()
	c.readPump()
}

func (h *Hub) RunSubscriber(ctx context.Context) {
	for ctx.Err() == nil {
		pubsub, err := h.redis.Subscribe(ctx)
		if err != nil {
			h.logger.Error("Redis Pub/Sub subscription failed", "error", err)
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		for ctx.Err() == nil {
			message, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				if ctx.Err() == nil {
					h.logger.Error("Redis Pub/Sub receive failed", "error", err)
				}
				break
			}
			showID := showIDFromChannel(message.Channel)
			if showID == "" || !json.Valid([]byte(message.Payload)) {
				h.logger.Warn("discarding malformed show event", "channel", message.Channel)
				continue
			}
			h.broadcast(showID, []byte(message.Payload))
		}
		if err := pubsub.Close(); err != nil && ctx.Err() == nil {
			h.logger.Error("close Redis Pub/Sub subscription", "error", err)
		}
		if !wait(ctx, time.Second) {
			return
		}
	}
}

func (h *Hub) add(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if h.rooms[c.showID] == nil {
		h.rooms[c.showID] = make(map[*client]struct{})
	}
	h.rooms[c.showID][c] = struct{}{}
	h.clients.Add(1)
	return true
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.rooms[c.showID]
	if _, ok := room[c]; !ok {
		return
	}
	delete(room, c)
	close(c.send)
	if len(room) == 0 {
		delete(h.rooms, c.showID)
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*client, 0)
	for showID, room := range h.rooms {
		for c := range room {
			clients = append(clients, c)
			close(c.send)
		}
		delete(h.rooms, showID)
	}
	h.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(writeWait))
		_ = c.conn.Close()
	}
	h.clients.Wait()
}

func (h *Hub) broadcast(showID string, message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.rooms[showID] {
		select {
		case c.send <- message:
		default:
			go h.dropSlowClient(c)
		}
	}
}

func (h *Hub) dropSlowClient(c *client) {
	h.remove(c)
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "client is too slow"),
		time.Now().Add(writeWait))
	_ = c.conn.Close()
}

func (h *Hub) publishViewerCount(ctx context.Context, showID string, count int64) error {
	eventID, err := id.New()
	if err != nil {
		return err
	}
	return h.redis.Publish(ctx, showID, model.Event{
		ID: eventID, Type: "VIEWER_COUNT", ShowID: showID, Count: &count,
	})
}

func (c *client) readPump() {
	defer func() {
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}

func showIDFromChannel(channel string) string {
	if !strings.HasPrefix(channel, "show:") || !strings.HasSuffix(channel, ":events") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(channel, "show:"), ":events")
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
