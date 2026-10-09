import { useSyncExternalStore } from "react";

import { currentRealtimeStatus, subscribeRealtimeStatus } from "../lib/realtime";

/*
 * The console socket's state, read where it is shown.
 *
 * `useSyncExternalStore` is the right hook for a value that lives outside React
 * and changes from a socket callback: it reads the current value during render
 * and subscribes after it, so a reconnect that lands between the two is not
 * missed.
 */
export function useRealtimeStatus() {
  return useSyncExternalStore(subscribeRealtimeStatus, currentRealtimeStatus, currentRealtimeStatus);
}
