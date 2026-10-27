/*
 * The audit-side locate action.
 *
 * A finding names a node, not the bytes that proved it, and the findings table
 * stores a redacted excerpt for personal data. The endpoint therefore recomputes
 * the match from the cassette on demand and returns it for this one read: the
 * value is highlighted in the conversation but never travels in the URL (that
 * would put a personal number in the address bar and the browser history), so
 * the caller keeps it in component state.
 */
import { apiPaths, requestJSON } from "./api.js";

export function traceFindingLocateURL(traceID, findingID) {
  return apiPaths.traceFindingLocate(traceID, findingID);
}

/**
 * locateFindingInConversation asks where one finding was proven.
 *
 * @param {string} traceID
 * @param {string} findingID
 * @param {{signal?: AbortSignal}} [options]
 * @returns {Promise<{anchor: string, value: string, payload: any}>}
 */
export async function locateFindingInConversation(traceID, findingID, options = {}) {
  if (!traceID || !findingID) {
    return { anchor: "", value: "", payload: null };
  }
  const payload = await requestJSON(traceFindingLocateURL(traceID, findingID), options);
  return {
    anchor: payload?.anchor || "",
    value: payload?.value || "",
    payload,
  };
}
