import { useQueryClient } from "@tanstack/react-query";

/**
 * Refetch everything currently on screen.
 *
 * Pages used to keep a `refreshTick` counter and bump it after a write so the
 * dependent reads would run again. Invalidating the cache does the same thing
 * without the counter, and it also picks up queries the writer does not know
 * about - the sidebar's event summary, a detail panel another component owns.
 */
export function useRefresh() {
  const client = useQueryClient();
  return () => client.invalidateQueries();
}
