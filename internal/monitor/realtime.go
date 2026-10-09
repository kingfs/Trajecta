package monitor

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kingfs/Trajecta/internal/store"
)

/*
 * The console's realtime socket.
 *
 * Every reading the pages used to poll now arrives as a push. There is one
 * socket per browser tab at GET /api/events/ws; the browser announces which
 * topics it is showing and the server sends a short message whenever one of
 * them changes. The message names the topic and carries no data, so a change
 * never re-serialises a page's worth of records and two tabs cannot disagree
 * about the shape of anything - they refetch what they already know how to
 * render, through the same HTTP endpoints they render it from today.
 *
 * Topics:
 *
 *   traffic - a request was indexed, parsed, or analysed
 *   events  - the runtime event feed changed
 *   system  - the host, process and database samples are due again, which is
 *             the one genuinely periodic reading in the console. It is sampled
 *             here, on one timer shared by every open tab, and only while a tab
 *             is actually showing it; a browser that is not on a system page
 *             cannot make the server read process and database statistics.
 *
 * The one message the browser sends is {"topics":["traffic","events"]}.
 */

// realtimeSocketPath is the console socket, and the path the browser may
// authenticate from its query string; see allowMonitorQueryAccessToken.
const realtimeSocketPath = "/api/events/ws"

const (
	realtimeTopicTraffic = "traffic"
	realtimeTopicEvents  = "events"
	realtimeTopicSystem  = "system"
)

// realtimeTopics is the set a client may subscribe to. An unknown topic in a
// subscribe message is dropped rather than answered with an error: the console
// ships a new topic with the server that publishes it, and a stale bundle should
// keep working instead of failing its whole connection.
var realtimeTopics = map[string]bool{
	realtimeTopicTraffic: true,
	realtimeTopicEvents:  true,
	realtimeTopicSystem:  true,
}

const (
	// realtimeSystemSampleInterval is the tick the system page's charts are
	// drawn at. It is five seconds because the page plots a five-minute trend
	// from the samples the server takes while a tab is watching, and a coarser
	// tick would draw a straight line through the spike a reader opened the page
	// to see. The tick only runs while a tab subscribes, so an unwatched server
	// samples nothing, and the PostgreSQL panels on that page only refetch while
	// they are the visible tab.
	realtimeSystemSampleInterval = 5 * time.Second
	realtimePingInterval         = 25 * time.Second
	realtimePongWait             = 70 * time.Second
	realtimeWriteWait            = 10 * time.Second
	realtimeMaxMessageSize       = 4 << 10
)

var realtimeUpgrader = websocket.Upgrader{
	// The console is served from the same origin as the socket, and the monitor
	// is regularly put behind a reverse proxy on another host, so the origin is
	// checked by the auth layer that already gates every other route rather than
	// by the default same-origin rule, which would break exactly those proxies.
	CheckOrigin: func(*http.Request) bool { return true },
}

type realtimeHub struct {
	store *store.Store

	// sampleInterval is a field rather than the constant so a test can drive the
	// one server-side timer without waiting half a minute for it.
	sampleInterval time.Duration

	mu       sync.Mutex
	clients  map[*realtimeClient]struct{}
	sampling bool
	// unsubscribe is non-nil exactly while a forwarding goroutine is reading the
	// store's change notifications, which is exactly while a tab is connected.
	unsubscribe func()
}

func newRealtimeHub(st *store.Store) *realtimeHub {
	return &realtimeHub{store: st, clients: map[*realtimeClient]struct{}{}, sampleInterval: realtimeSystemSampleInterval}
}

// publish fans one topic out to every client that subscribed to it.
func (h *realtimeHub) publish(topics ...string) {
	h.mu.Lock()
	targets := make(map[*realtimeClient][]string, len(h.clients))
	for client := range h.clients {
		for _, topic := range topics {
			if client.topics[topic] {
				targets[client] = append(targets[client], topic)
			}
		}
	}
	h.mu.Unlock()
	for client, matched := range targets {
		for _, topic := range matched {
			client.send(topic)
		}
	}
}

// add registers a client and, on the first one, starts forwarding store changes.
func (h *realtimeHub) add(client *realtimeClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = struct{}{}
	h.syncForwardingLocked()
}

// remove deregisters a client, stopping the store subscription once the last tab
// is gone so an idle server holds no listener.
func (h *realtimeHub) remove(client *realtimeClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, client)
	h.syncForwardingLocked()
}

// syncForwardingLocked keeps the store subscription in step with the number of
// connected tabs. It is only ever called with h.mu held.
func (h *realtimeHub) syncForwardingLocked() {
	if len(h.clients) > 0 && h.unsubscribe == nil && h.store != nil {
		changes, unsubscribe := h.store.SubscribeChanges(64)
		h.unsubscribe = unsubscribe
		go func() {
			for topic := range changes {
				h.publish(topic)
			}
		}()
		return
	}
	if len(h.clients) == 0 && h.unsubscribe != nil {
		unsubscribe := h.unsubscribe
		h.unsubscribe = nil
		// Closing the channel ends the forwarding goroutine, so a server with no
		// open console has no realtime work at all.
		unsubscribe()
	}
}

// setTopics replaces one client's subscription set and answers it with one push
// per subscribed topic.
//
// That first push is not an acknowledgement, it is the catch-up: signals are
// dropped when they cannot be delivered and the server only reports changes it
// publishes after the subscription exists, so a tab that subscribes while
// traffic is being recorded would otherwise have to wait for the next request to
// arrive before it saw any of it. Answering the subscription with its own topics
// means "fetch what you are showing now", which is what the tab does on connect
// anyway.
func (h *realtimeHub) setTopics(client *realtimeClient, topics []string) {
	h.mu.Lock()
	client.topics = map[string]bool{}
	subscribed := make([]string, 0, len(topics))
	for _, topic := range topics {
		if realtimeTopics[topic] && !client.topics[topic] {
			client.topics[topic] = true
			subscribed = append(subscribed, topic)
		}
	}
	// The system sample is the only topic the server produces on its own, so it
	// is the only one that needs a timer, and only while someone is watching.
	if h.hasTopicLocked(realtimeTopicSystem) {
		h.startSamplerLocked()
	}
	h.mu.Unlock()
	for _, topic := range subscribed {
		client.send(topic)
	}
}

func (h *realtimeHub) hasTopicLocked(topic string) bool {
	for client := range h.clients {
		if client.topics[topic] {
			return true
		}
	}
	return false
}

func (h *realtimeHub) startSamplerLocked() {
	if h.sampling {
		return
	}
	h.sampling = true
	go h.sampleLoop()
}

func (h *realtimeHub) sampleLoop() {
	ticker := time.NewTicker(h.sampleInterval)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		active := h.hasTopicLocked(realtimeTopicSystem)
		if !active {
			// The last tab left the system page, so the timer stops with it; the
			// next subscription starts a new one.
			h.sampling = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		// Take the reading before publishing it: the host sample is what the
		// trend is made of, and the push is what makes the open tab fetch it.
		// Sampling here rather than in the subscriber means every tab on the
		// page draws the same series, and it is the reason the series exists at
		// all when the reader has only just opened the page.
		buildSystemHost(time.Now())
		h.publish(realtimeTopicSystem)
	}
}

type realtimeClient struct {
	hub  *realtimeHub
	conn *websocket.Conn

	// mu guards out and closed. Every send to the socket goes through out, so
	// the single writer goroutine owns the connection - a websocket allows one
	// concurrent writer - and the closed flag is what makes a publish racing
	// with a disconnect impossible rather than merely unlikely.
	mu     sync.Mutex
	out    chan string
	closed bool

	// topics is guarded by the hub's mutex, not by mu, so a subscribe can be
	// resolved against the client set without taking two locks.
	topics map[string]bool
}

func (h *realtimeHub) newClient(conn *websocket.Conn) *realtimeClient {
	return &realtimeClient{hub: h, conn: conn, out: make(chan string, 16), topics: map[string]bool{}}
}

func (c *realtimeClient) send(topic string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.out <- topic:
	default:
		// A tab that cannot keep up with the signal loses the signal, not the
		// connection: the next change publishes the next one.
	}
}

func (c *realtimeClient) close() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.out)
	}
	c.mu.Unlock()
}

// realtimeSocketAPIHandler upgrades one request into the realtime socket and
// serves it until the browser goes away.
func realtimeSocketAPIHandler(hub *realtimeHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		conn, err := realtimeUpgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade has already written the HTTP error.
			return
		}
		client := hub.newClient(conn)
		hub.add(client)
		defer func() {
			hub.remove(client)
			client.close()
			_ = conn.Close()
		}()

		conn.SetReadLimit(realtimeMaxMessageSize)
		_ = conn.SetReadDeadline(time.Now().Add(realtimePongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(realtimePongWait))
		})

		done := make(chan struct{})
		defer close(done)
		go client.writeLoop(done)
		client.readLoop()
	}
}

// readLoop consumes the browser's subscribe messages until the connection ends.
// The read deadline is what notices a browser that vanished without a close
// frame - a laptop that went to sleep, a network that dropped - and the pong
// handler extends it while the browser answers the pings.
func (c *realtimeClient) readLoop() {
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var message struct {
			Topics []string `json:"topics"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			// A malformed frame is a client bug, not a reason to drop a working
			// connection; the subscription simply stays as it was.
			continue
		}
		if message.Topics != nil {
			c.hub.setTopics(c, message.Topics)
		}
	}
}

// writeLoop is the only writer on the connection: it interleaves the topic
// messages with the pings that keep the read deadline alive.
func (c *realtimeClient) writeLoop(done <-chan struct{}) {
	pings := time.NewTicker(realtimePingInterval)
	defer pings.Stop()
	for {
		select {
		case <-done:
			return
		case topic, ok := <-c.out:
			if !ok {
				return
			}
			payload, err := json.Marshal(map[string]string{"topic": topic})
			if err != nil {
				continue
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(realtimeWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-pings.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(realtimeWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
