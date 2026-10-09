import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  RealtimeStatus,
  realtimeSocketURL,
  registeredRealtimeQueries,
  registeredRealtimeTopics,
  setRealtimeStatus,
  subscribeRealtimeRegistry,
} from "../lib/realtime";

/*
 * One WebSocket for the whole console, and the refetch it triggers.
 *
 * The provider renders nothing. It keeps a socket open, tells the server which
 * topics the mounted pages care about (see lib/realtime.js for the registry),
 * and on a topic message invalidates exactly the query keys registered under
 * that topic - so a page refetches its own reads and nothing else.
 *
 * Two properties matter more than the happy path:
 *
 *   - A signal is a hint, not a fact, so bursts are coalesced. Indexing a batch
 *     of cassettes publishes one signal per file; refetching per signal would
 *     make the console hammer itself while it is busy. Signals are collected
 *     for a moment and the topics are refetched once.
 *   - A missed signal must not be a permanent gap. The server answers a
 *     subscription with one push per subscribed topic, and the client re-sends
 *     its subscription on every reconnect, so a socket that was down catches up
 *     as soon as it is back. The reconnect backs off exponentially so a stopped
 *     server does not collect a connection attempt per second forever.
 */

const COALESCE_MS = 150;
const RECONNECT_MIN_MS = 1_000;
// The cap the console settles on when the server stays away: one attempt every
// five minutes is enough to come back on its own without a stopped server
// collecting an attempt per second, and it is why a reader who restarts a
// server overnight does not have to remember to reload the page.
const RECONNECT_MAX_MS = 300_000;

export function RealtimeProvider({ children = null }) {
  const queryClient = useQueryClient();
  const socketRef = useRef(null);

  useEffect(() => {
    let disposed = false;
    let socket = null;
    let reconnectTimer = 0;
    let flushTimer = 0;
    let sendTimer = 0;
    let retryDelay = RECONNECT_MIN_MS;
    const pending = new Set();

    const flush = () => {
      flushTimer = 0;
      const topics = [...pending];
      pending.clear();
      for (const topic of topics) {
        for (const queryKey of registeredRealtimeQueries(topic)) {
          queryClient.invalidateQueries({ queryKey });
        }
      }
    };

    const signal = (topic) => {
      pending.add(topic);
      if (flushTimer) {
        return;
      }
      flushTimer = window.setTimeout(flush, COALESCE_MS);
    };

    const sendSubscription = () => {
      sendTimer = 0;
      if (!socket || socket.readyState !== WebSocket.OPEN) {
        return;
      }
      socket.send(JSON.stringify({ topics: registeredRealtimeTopics() }));
    };

    // Registration is presence, not state: a page mounting a new topic only has
    // to tell the server, and the server answers with a catch-up push for it.
    const scheduleSubscription = () => {
      if (sendTimer) {
        return;
      }
      sendTimer = window.setTimeout(sendSubscription, 0);
    };

    const connect = () => {
      if (disposed) {
        return;
      }
      setRealtimeStatus(RealtimeStatus.CONNECTING);
      const opened = new WebSocket(realtimeSocketURL());
      socket = opened;
      socketRef.current = opened;

      opened.onopen = () => {
        retryDelay = RECONNECT_MIN_MS;
        setRealtimeStatus(RealtimeStatus.CONNECTED);
        sendSubscription();
      };
      opened.onmessage = (event) => {
        let topic = "";
        try {
          topic = JSON.parse(event.data)?.topic || "";
        } catch {
          return;
        }
        if (topic) {
          signal(topic);
        }
      };
      opened.onclose = () => {
        if (socket === opened) {
          socketRef.current = null;
        }
        setRealtimeStatus(RealtimeStatus.RETRYING);
        if (disposed) {
          return;
        }
        // The console has no refresh timers, so a socket that cannot connect is
        // the one failure a reader cannot see: say it once, with the delay the
        // retry will use, and let the sidebar indicator carry the state from
        // there.
        console.warn(
          `[realtime] the console socket closed; retrying /api/events/ws in ${retryDelay}ms (cap 300000ms)`,
        );
        reconnectTimer = window.setTimeout(connect, retryDelay);
        retryDelay = Math.min(retryDelay * 2, RECONNECT_MAX_MS);
      };
      // A failed connection is reported through onclose, which is where the
      // reconnect lives; onerror only exists to keep the browser from logging an
      // unhandled event.
      opened.onerror = () => {};
    };

    connect();
    const unsubscribeRegistry = subscribeRealtimeRegistry(scheduleSubscription);

    return () => {
      disposed = true;
      setRealtimeStatus(RealtimeStatus.CONNECTING);
      unsubscribeRegistry();
      window.clearTimeout(reconnectTimer);
      window.clearTimeout(flushTimer);
      window.clearTimeout(sendTimer);
      if (socket) {
        socket.onclose = null;
        socket.onmessage = null;
        socket.onopen = null;
        socket.onerror = null;
        socket.close();
      }
      socketRef.current = null;
    };
  }, [queryClient]);

  return children;
}
