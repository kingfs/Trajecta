import { useCallback, useMemo, useState } from "react";

import { useJSON } from "./useJSON";
import { apiPaths } from "../lib/api";

/*
 * One window of a session's step model, shared by the trace page's 轨迹 and 统计
 * tabs.
 *
 * Both tabs read the same endpoint, so they issue one request between them:
 * `useJSON` is TanStack Query underneath and the query key is the URL. Paging
 * works by widening the window (`limit`) rather than by splicing pages
 * together, which keeps the turn numbering and the session-wide totals in the
 * server's hands; the server bounds the window, and its cassette pass is what
 * makes a wide window expensive.
 */

export const STEP_PAGE_SIZE = 100;

export function useSessionSteps({ traceID, sessionID }) {
  const [limit, setLimit] = useState(STEP_PAGE_SIZE);
  // The trace-scoped route wins when both are known: it ends the window at
  // *this* trace, while the session route ends it at the newest one. Opening
  // trace #40 of a 500-step session must show the steps that led to #40.
  const url = useMemo(() => {
    if (traceID) return apiPaths.traceSessionSteps(traceID, { limit });
    if (sessionID) return apiPaths.sessionSteps(sessionID, { limit });
    return null;
  }, [sessionID, traceID, limit]);
  const { data, loading, error } = useJSON(url, [limit]);
  const hasBefore = Boolean(data?.window?.has_before);
  const canLoadOlder = hasBefore && limit < 500;
  const loadOlder = useCallback(() => {
    setLimit((current) => Math.min(current + STEP_PAGE_SIZE, 500));
  }, []);
  return { view: data, loading, error, hasBefore, canLoadOlder, loadOlder, limit };
}
