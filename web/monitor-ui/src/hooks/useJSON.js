import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { requestJSON } from "../lib/api";

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
 * `options.refetchInterval` replaces the `refreshTick` state and `setInterval`
 * pair that every polling page used to carry: pass it and the page drops both.
 */
export function useJSON(url, deps = [], options = {}) {
  // A falsy url parks the hook: nothing is requested and the state stays idle.
  // Callers use it to wait for a parent resource before firing sub-resources.
  const enabled = typeof url === "string" && url !== "";
  const { refetchInterval, ...rest } = options;

  const query = useQuery({
    queryKey: ["GET", url, ...deps],
    queryFn: ({ signal }) => requestJSON(url, { signal }),
    enabled,
    refetchInterval,
    // Hold the previous key's data while the next one loads. Without this a
    // window switch blanks the page for a moment, which the old hook did not do.
    placeholderData: keepPreviousData,
    ...rest,
  });

  return {
    // `isPending` is true only while there is nothing to show for the current
    // key: a background refetch must not replace a rendered page with a spinner.
    loading: enabled && query.isPending,
    data: query.data ?? null,
    error: query.error ? query.error.message || "unknown error" : "",
  };
}
