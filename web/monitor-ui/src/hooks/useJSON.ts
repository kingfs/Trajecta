import { useEffect, useRef } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { requestJSON } from "../lib/api";
import { realtimeTopicForURL, registerRealtimeQuery } from "../lib/realtime";

/**
 * Read a Monitor endpoint.
 *
 * This used to be a `useEffect` + `AbortController` + `useState` trio, one per
 * call site, with no cache: two components reading the same URL on one screen
 * made two requests, and every remount started from an empty page. It is now a
 * thin wrapper over TanStack Query, which also brings request de-duplication,
 * cancellation on unmount or key change, and a cache that a page can come back
 * to.
 *
 * The signature and the returned shape are unchanged on purpose, so the 34 call
 * sites that already exist did not have to move: `useJSON(url, deps)` returns
 * `{ loading, data, error }`.
 *
 * `deps` are the request's identity beyond the URL. Most callers pass values
 * that are already in the query string, which is harmless; the ones that matter
 * are the window and filter selections.
 *
 * Nothing here polls. A read whose URL belongs to a realtime topic registers
 * itself with the console socket (lib/realtime.js), and the page is refetched
 * when the server says that topic changed - which replaced a `refetchInterval`
 * on eight reads and a `setInterval` in the sidebar.
 */
export type UseJSONOptions = {
  /** Poll interval in ms, or false to stop polling. */
  refetchInterval?: number | false;
  /** Forwarded to TanStack Query; `staleTime` and `enabled` are the ones used. */
  [key: string]: unknown;
};

export function useJSON<T = unknown>(
  url: string | null | undefined,
  deps: readonly unknown[] = [],
  options: UseJSONOptions = {},
) {
  // A falsy url parks the hook: nothing is requested and the state stays idle.
  // Callers use it to wait for a parent resource before firing sub-resources.
  const enabled = typeof url === "string" && url !== "";
  const { refetchInterval, ...rest } = options;
  const queryKey = ["GET", url, ...deps];

  const query = useQuery<T>({
    queryKey,
    queryFn: ({ signal }) => requestJSON(url as string, { signal }),
    enabled,
    refetchInterval,
    // Hold the previous key's data while the next one loads. Without this a
    // window switch blanks the page for a moment, which the old hook did not do.
    placeholderData: keepPreviousData,
    ...rest,
  });

  // The topic is derived from the URL, so a read is subscribed as soon as it is
  // mounted and unsubscribed when it is not. The key is held in a ref because
  // the query key array is rebuilt on every render while its identity - the
  // request - is what the registry has to track.
  const topic = enabled ? realtimeTopicForURL(url as string) : "";
  const registrationKey = topic ? JSON.stringify(queryKey) : "";
  const queryKeyRef = useRef(queryKey);
  queryKeyRef.current = queryKey;
  useEffect(() => {
    if (!topic) {
      return undefined;
    }
    return registerRealtimeQuery(topic, queryKeyRef.current);
  }, [topic, registrationKey]);

  return {
    // `isPending` is true only while there is nothing to show for the current
    // key: a background refetch must not replace a rendered page with a spinner.
    loading: enabled && query.isPending,
    data: query.data ?? null,
    error: query.error ? query.error.message || "unknown error" : "",
  };
}
