package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

// The realtime socket is the transport every page's freshness now depends on, so
// these tests drive it over a real WebSocket against a real store rather than
// against the hub's methods: the properties that matter (a subscribe selects the
// topics a browser receives, an unsubscribed topic is silent, the server stops
// listening once the last tab leaves, the browser can authenticate from the query
// string) are all properties of the assembled handler.
func newRealtimeTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// realtimeTestPlainTextVerifier accepts exactly one token, so the test can show
// that the socket's query-string path is the whole authentication.
type realtimeTokenVerifier struct{ token string }

func (v realtimeTokenVerifier) VerifyToken(_ context.Context, token string) (auth.Principal, bool, error) {
	if token == "" || token != v.token {
		return auth.Principal{}, false, nil
	}
	return auth.Principal{Username: "tester", Role: "admin"}, true, nil
}

func dialRealtimeSocket(t *testing.T, base string, query string) *websocket.Conn {
	t.Helper()
	socketURL := "ws" + strings.TrimPrefix(base, "http") + realtimeSocketPath + query
	conn, response, err := websocket.DefaultDialer.Dial(socketURL, nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial %s error = %v (status %d)", socketURL, err, status)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func subscribeRealtimeTopics(t *testing.T, conn *websocket.Conn, topics ...string) {
	t.Helper()
	payload, err := json.Marshal(map[string][]string{"topics": topics})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
}

// readRealtimeTopics collects the topics pushed until it has seen every wanted
// one or the deadline passes. Reading is cumulative: the order the topics arrive
// in is not part of the contract.
func readRealtimeTopics(t *testing.T, conn *websocket.Conn, want ...string) map[string]bool {
	t.Helper()
	remaining := map[string]bool{}
	for _, topic := range want {
		remaining[topic] = true
	}
	seen := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for len(remaining) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for topics %v, saw %v", remaining, seen)
		}
		_ = conn.SetReadDeadline(deadline)
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message error = %v, saw %v", err, seen)
		}
		var message struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode %q error = %v", payload, err)
		}
		seen[message.Topic] = true
		delete(remaining, message.Topic)
	}
	return seen
}

func TestRealtimeSocketPushesOnlySubscribedTopics(t *testing.T) {
	t.Parallel()
	st := newRealtimeTestStore(t)
	hub := newRealtimeHub(st)
	// The system sample is the one server-side timer; shortening it keeps the
	// test in milliseconds without weakening what it asserts.
	hub.sampleInterval = 20 * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc(realtimeSocketPath, realtimeSocketAPIHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialRealtimeSocket(t, server.URL, "")
	subscribeRealtimeTopics(t, conn, realtimeTopicTraffic, realtimeTopicSystem)

	// A write the store publishes "traffic" for, and the sample timer, which needs
	// no write at all.
	if _, err := st.SaveAnalysisRun(store.AnalysisRunRecord{Kind: "test", Analyzer: "test"}); err != nil {
		t.Fatalf("SaveAnalysisRun() error = %v", err)
	}
	seen := readRealtimeTopics(t, conn, realtimeTopicTraffic, realtimeTopicSystem)
	if !seen[realtimeTopicTraffic] || !seen[realtimeTopicSystem] {
		t.Fatalf("pushed topics = %v, want traffic and system", seen)
	}

	// An event the client did not subscribe to is not sent: a browser on the
	// overview page must not be woken by a topic it does not render.
	if _, err := st.UpsertSystemEvent(store.SystemEvent{
		Fingerprint: "realtime-test:events",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "warning",
		Title:       "realtime test",
	}); err != nil {
		t.Fatalf("UpsertSystemEvent() error = %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err) {
				t.Fatalf("unexpected close: %v", err)
			}
			break
		}
		var message struct {
			Topic string `json:"topic"`
		}
		_ = json.Unmarshal(payload, &message)
		if message.Topic == realtimeTopicEvents {
			t.Fatalf("received an unsubscribed topic: %s", message.Topic)
		}
	}
}

func TestRealtimeSocketStopsListeningWhenTheLastTabLeaves(t *testing.T) {
	t.Parallel()
	st := newRealtimeTestStore(t)
	hub := newRealtimeHub(st)

	mux := http.NewServeMux()
	mux.HandleFunc(realtimeSocketPath, realtimeSocketAPIHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialRealtimeSocket(t, server.URL, "")
	subscribeRealtimeTopics(t, conn, realtimeTopicTraffic)

	// Wait for the store subscription to appear rather than racing the handler.
	waitFor(t, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.unsubscribe != nil
	}, "the store subscription was never taken")

	if err := conn.Close(); err != nil {
		t.Fatalf("close socket: %v", err)
	}
	// No open tab keeps the store subscription; an idle console costs nothing.
	waitFor(t, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.unsubscribe == nil && len(hub.clients) == 0
	}, "the store subscription outlived the last tab")
}

func TestRealtimeSocketAuthenticatesFromTheQueryString(t *testing.T) {
	t.Parallel()
	st := newRealtimeTestStore(t)
	hub := newRealtimeHub(st)
	verifier := realtimeTokenVerifier{token: "console-token"}

	mux := http.NewServeMux()
	mux.HandleFunc(realtimeSocketPath, monitorAuthRequired(realtimeSocketAPIHandler(hub), verifier))
	server := httptest.NewServer(mux)
	defer server.Close()

	// A browser cannot set an Authorization header on a WebSocket, so the token
	// it holds has to be accepted from the query string.
	conn := dialRealtimeSocket(t, server.URL, "?access_token="+url.QueryEscape("console-token"))
	subscribeRealtimeTopics(t, conn, realtimeTopicTraffic)
	if _, err := st.SaveAnalysisRun(store.AnalysisRunRecord{Kind: "test", Analyzer: "test"}); err != nil {
		t.Fatalf("SaveAnalysisRun() error = %v", err)
	}
	readRealtimeTopics(t, conn, realtimeTopicTraffic)

	// Without a token the upgrade never happens.
	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + realtimeSocketPath
	if _, response, err := websocket.DefaultDialer.Dial(socketURL, nil); err == nil {
		t.Fatal("dial without a token succeeded")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial without a token status = %d, want 401", status)
	}
}

// waitFor polls a condition with a deadline so the test does not depend on how
// long the handler takes to notice a closed socket.
func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", message)
}
