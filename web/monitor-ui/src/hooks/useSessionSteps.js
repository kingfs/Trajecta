import { useCallback, useEffect, useMemo, useState } from "react";

import { useJSON } from "./useJSON";
import { apiPaths, requestJSON } from "../lib/api";
import { mergeViews } from "../lib/sessionSteps";

/*
 * One window of a session's step model, shared by the trace page's 轨迹 and 统计
 * tabs and by the session page.
 *
 * The first page is fetched by `useJSON`, which gives it the query cache and the
 * realtime invalidation every other read has. Older and newer pages are fetched
 * on demand and merged around it, so a session longer than one window can be
 * walked in both directions without a rebuild of the whole session: the server
 * keeps returning one page at a time and the merge renumbers the turns so the
 * list stays contiguous.
 *
 * Paging is by trace id, never by offset: a trace recorded while the page is
 * open must not shift the window (that is the classic reason a "load more"
 * button repeats or skips a row).
 */

export const STEP_PAGE_SIZE = 100;

export function useSessionSteps({ traceID, sessionID }) {
  const baseURL = useMemo(() => {
    if (traceID) return apiPaths.traceSessionSteps(traceID, { limit: STEP_PAGE_SIZE });
    if (sessionID) return apiPaths.sessionSteps(sessionID, { limit: STEP_PAGE_SIZE });
    return null;
  }, [traceID, sessionID]);
  const { data: firstPage, loading, error } = useJSON(baseURL, [traceID, sessionID]);

  // `older` is ordered oldest first, `newer` newest last, so the concatenation
  // below reads as one session.
  const [older, setOlder] = useState([]);
  const [newer, setNewer] = useState([]);
  const [pending, setPending] = useState("");
  const [pageError, setPageError] = useState("");

  // A different trace or session is a different window; pages loaded for the
  // previous one must not be spliced into it.
  useEffect(() => {
    setOlder([]);
    setNewer([]);
    setPageError("");
    setPending("");
  }, [baseURL]);

  const views = useMemo(() => {
    if (!firstPage) return [];
    return [...older, firstPage, ...newer];
  }, [older, firstPage, newer]);
  const view = useMemo(() => mergeViews(views), [views]);

  const fetchPage = useCallback(
    async (cursor) => {
      const path = traceID
        ? apiPaths.traceSessionSteps(traceID, { limit: STEP_PAGE_SIZE, ...cursor })
        : apiPaths.sessionSteps(sessionID, { limit: STEP_PAGE_SIZE, ...cursor });
      return requestJSON(path);
    },
    [traceID, sessionID],
  );

  const loadOlder = useCallback(async () => {
    if (pending) return;
    const before = boundaryTraceID(views[0], "first");
    if (!before) return;
    setPending("older");
    setPageError("");
    try {
      const page = await fetchPage({ before });
      setOlder((current) => [page, ...current]);
    } catch (failure) {
      setPageError(failure?.message || String(failure));
    } finally {
      setPending("");
    }
  }, [fetchPage, pending, views]);

  const loadNewer = useCallback(async () => {
    if (pending) return;
    const after = boundaryTraceID(views[views.length - 1], "last");
    if (!after) return;
    setPending("newer");
    setPageError("");
    try {
      const page = await fetchPage({ after });
      setNewer((current) => [...current, page]);
    } catch (failure) {
      setPageError(failure?.message || String(failure));
    } finally {
      setPending("");
    }
  }, [fetchPage, pending, views]);

  return {
    view,
    loading: loading && !view,
    error: error || pageError,
    // The first page is a prefetch; a failed prefetch leaves nothing to show.
    canLoadOlder: Boolean(view?.window?.has_before) && pending !== "older",
    canLoadNewer: Boolean(view?.window?.has_after) && pending !== "newer",
    loadingOlder: pending === "older",
    loadingNewer: pending === "newer",
    loadOlder,
    loadNewer,
  };
}

/** The first or last step's trace id, used as a paging cursor. */
function boundaryTraceID(view, side) {
  const turn = side === "first" ? view?.turns?.[0] : view?.turns?.[view.turns.length - 1];
  const steps = turn?.steps || [];
  return side === "first" ? steps[0]?.trace_id || "" : steps[steps.length - 1]?.trace_id || "";
}
