// The explicit extension is what lets the unit tests import this module in
// node, which does not resolve extensionless specifiers.
import { MONITOR_TOKEN_KEY, apiPaths } from "./api.js";

/*
 * The realtime socket, from the browser's side.
 *
 * Every page used to poll: `refetchInterval: 60_000` on eight reads, plus a
 * `setInterval` in the sidebar, plus a second polling loop inside the SSE
 * reconnect. The console now opens one WebSocket, tells the server which topics
 * it is showing, and refetches a read when the server says that read's topic
 * changed.
 *
 * What a topic is, and which reads belong to it, is the one piece of knowledge
 * both halves have to agree on. It is written down here as URL prefixes rather
 * than as a list of call sites, because the call site is TanStack Query: every
 * Monitor read goes through `useJSON`, the query key carries the request URL,
 * and the URL already says what the read is about. `useJSON` therefore registers
 * itself, and a page gets push-freshness without knowing the socket exists.
 */

/**
 * Topic -> the request paths it covers. A read matches the longest prefix it
 * starts with, so a narrower topic added later wins over a broader one.
 */
export const REALTIME_TOPIC_PREFIXES = {
  // Every read a recorded request moves: the lists, the aggregates, the
  // findings, the analysis queue, and the routing and upstream views that are
  // derived from the same records.
  traffic: [
    "/api/traces",
    "/api/sessions",
    "/api/overview",
    "/api/findings",
    "/api/analysis",
    "/api/routing",
    "/api/upstreams",
    "/api/models",
    "/api/channels",
  ],
  // The runtime event feed, which is what the sidebar badge and the events page
  // read.
  events: ["/api/events"],
  // Host, process and database samples. The server samples these on one timer
  // shared by every open tab, and only while a tab is showing them.
  system: ["/api/system"],
};

/** The topic a request URL belongs to, or "" for a read nothing pushes for. */
export function realtimeTopicForURL(url) {
  if (typeof url !== "string" || !url.startsWith("/")) {
    return "";
  }
  let match = "";
  let matchLength = -1;
  for (const [topic, prefixes] of Object.entries(REALTIME_TOPIC_PREFIXES)) {
    for (const prefix of prefixes) {
      if (url.startsWith(prefix) && prefix.length > matchLength) {
        match = topic;
        matchLength = prefix.length;
      }
    }
  }
  return match;
}

/** The socket URL, with the token in the query string a browser can send. */
export function realtimeSocketURL(location = window.location, token = window.localStorage.getItem(MONITOR_TOKEN_KEY) || "") {
  const url = new URL(apiPaths.eventsSocket, location.href);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  if (token) {
    url.searchParams.set("access_token", token);
  }
  return url.toString();
}

/*
 * The registry of mounted reads, by topic.
 *
 * It lives outside React on purpose. The provider is the only component that
 * opens a socket, and it needs the union of the topics the mounted pages care
 * about; a context would mean every `useJSON` call site sits under a provider
 * and re-renders when any other page subscribes. A module-level map plus a
 * change listener keeps the socket out of the render path: registering and
 * unregistering are not state changes, they are presence.
 */
const registrations = new Map();
const registryListeners = new Set();
let nextRegistrationId = 1;

function notifyRegistryChanged() {
  for (const listener of registryListeners) {
    listener();
  }
}

/**
 * Register one mounted query under its topic. Returns the function that
 * unregisters it, which is what a `useEffect` cleanup returns.
 */
export function registerRealtimeQuery(topic, queryKey) {
  if (!topic || !queryKey) {
    return () => {};
  }
  let entries = registrations.get(topic);
  if (!entries) {
    entries = new Map();
    registrations.set(topic, entries);
  }
  const id = nextRegistrationId++;
  entries.set(id, queryKey);
  notifyRegistryChanged();
  return () => {
    const current = registrations.get(topic);
    if (!current || !current.has(id)) {
      return;
    }
    current.delete(id);
    if (current.size === 0) {
      registrations.delete(topic);
    }
    notifyRegistryChanged();
  };
}

/** Every topic with at least one mounted read, which is what the server is sent. */
export function registeredRealtimeTopics() {
  return [...registrations.keys()].sort();
}

/** The query keys currently registered for one topic. */
export function registeredRealtimeQueries(topic) {
  return [...(registrations.get(topic)?.values() ?? [])];
}

/** Listen for subscribe-set changes; returns the unsubscribe function. */
export function subscribeRealtimeRegistry(listener) {
  registryListeners.add(listener);
  return () => {
    registryListeners.delete(listener);
  };
}

/*
 * Connection state, for the indicator in the sidebar.
 *
 * The console has no refresh timers left, so "is anything pushing to me?" is a
 * question a reader can reasonably ask and cannot answer by watching a spinner.
 * The state is a module value with a listener set for the same reason the
 * registry is: the provider owns the socket and does not re-render on it, and
 * the one component that shows the state should not pull the whole app into a
 * render per reconnect.
 *
 * Three states, because the reader's next question differs: "connecting" means
 * wait, "retrying" means the server is not there and the console is backing off
 * to a five-minute cap, and "connected" means what is on screen is being kept
 * current. The indicator draws a different glyph for each and says which one it
 * is in its tooltip.
 */
export const RealtimeStatus = {
  /** An attempt is in flight: the first one, or the one a backoff scheduled. */
  CONNECTING: "connecting",
  /** The socket is open and the tab is subscribed. */
  CONNECTED: "connected",
  /** The socket closed and the next attempt is waiting out its backoff. */
  RETRYING: "retrying",
};

let realtimeStatus = RealtimeStatus.CONNECTING;
const statusListeners = new Set();

export function currentRealtimeStatus() {
  return realtimeStatus;
}

export function setRealtimeStatus(next) {
  if (realtimeStatus === next) {
    return;
  }
  realtimeStatus = next;
  for (const listener of statusListeners) {
    listener();
  }
}

export function subscribeRealtimeStatus(listener) {
  statusListeners.add(listener);
  return () => {
    statusListeners.delete(listener);
  };
}
