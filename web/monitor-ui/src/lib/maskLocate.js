/*
 * The audit-side locate for a replaced credential.
 *
 * The credential audit row knows the original bytes but not the node they came
 * from, so the server searches the current parse and returns every occurrence.
 * Every one of them is raised as a conversation step id: the reader asked to see
 * all the places the replaced value appears, not just the first.
 *
 * The value itself never enters the response or the URL; the row already
 * carries it and the caller keeps it in component state.
 */
import { apiPaths, requestJSON } from "./api.js";

export function traceMaskLocateURL(traceID, maskID) {
  return apiPaths.traceMaskLocate(traceID, maskID);
}

/**
 * locateMaskInConversation asks where a masked original value sits.
 *
 * @param {string} traceID
 * @param {string} maskID
 * @param {{signal?: AbortSignal}} [options]
 * @returns {Promise<{anchor: string, anchors: string[], payload: any}>}
 */
export async function locateMaskInConversation(traceID, maskID, options = {}) {
  if (!traceID || !maskID) {
    return { anchor: "", anchors: [], payload: null };
  }
  const payload = await requestJSON(traceMaskLocateURL(traceID, maskID), options);
  const anchors = [];
  for (const item of payload?.items || []) {
    if (item?.anchor && !anchors.includes(item.anchor)) {
      anchors.push(item.anchor);
    }
  }
  return {
    anchor: payload?.anchor || anchors[0] || "",
    anchors,
    payload,
  };
}
