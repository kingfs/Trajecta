import { QueryClient } from "@tanstack/react-query";

/*
 * One QueryClient for the whole console.
 *
 * The defaults are chosen to keep the behaviour the hand-written `useJSON` hook
 * already had, because 34 call sites depend on it:
 *
 *   - `retry: false` matches the old hook, which surfaced the first failure.
 *     Retrying would also mean a local server that is down gets hammered, and
 *     the error-state tests would see three attempts instead of one.
 *   - `refetchOnWindowFocus: false` matches the old hook, which only ever
 *     refetched on a timer or a filter change. Focus-refetching a dashboard that
 *     polls anyway just redraws the page under the reader.
 *   - `staleTime` deduplicates the requests two components on one screen make
 *     for the same URL (the events page and the sidebar both read the summary)
 *     without making anything feel cached.
 *   - `gcTime` keeps an unmounted page's data long enough that navigating back
 *     within it renders instantly, which is the visible win over the old hook.
 */
export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        refetchOnWindowFocus: false,
        staleTime: 5_000,
        gcTime: 5 * 60_000,
      },
    },
  });
}

export const queryClient = createQueryClient();
