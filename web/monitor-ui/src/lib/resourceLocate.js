/*
 * Jumping from a resource row to the conversation step that carried it.
 *
 * The resource index stores one row per value and no node reference: the
 * cassette is the source of truth for a detail view, and the value is found by
 * re-running the current detector over the current parse (the server's
 * /resources/locate read). This module turns that answer into the trace URL the
 * 会话 tab understands - `node` is the step anchor the conversation scrolls to
 * and opens, `hl` is the value to mark inside the rendered text.
 *
 * A metric that the server did not find (an old index row, or a parser change)
 * yields an empty href rather than a URL that silently lands nowhere.
 */
import { apiPaths, apiURL, requestJSON } from "./api.js";

// The conversation puts `hl` in the URL so the highlight survives a reload and
// can be shared. A pathological value (the index allows up to 8 KiB) is left out
// rather than pushed through a browser address bar.
export const MAX_HIGHLIGHT_LENGTH = 512;

export function traceResourceLocateURL(traceID, kind, value) {
  return apiURL(apiPaths.traceResourceLocate(traceID), { kind, value });
}

export function conversationLocateHref(traceID, anchor, value = "") {
  if (!traceID || !anchor) {
    return "";
  }
  const params = new URLSearchParams({ tab: "conversation", node: anchor });
  const highlight = String(value || "");
  if (highlight && highlight.length <= MAX_HIGHLIGHT_LENGTH) {
    params.set("hl", highlight);
  }
  return `/traces/${encodeURIComponent(traceID)}?${params.toString()}`;
}

/**
 * locateResourceConversation asks the server where a resource row was detected
 * and returns the conversation URL for it.
 *
 * @param {string} traceID
 * @param {{kind?: string, value?: string}} item
 * @param {{signal?: AbortSignal}} [options]
 * @returns {Promise<{href: string, anchor: string, highlight: string, payload: any}>}
 */
export async function locateResourceConversation(traceID, item, options = {}) {
  const value = String(item?.value || "");
  if (!traceID || !value) {
    return { href: "", anchor: "", highlight: "", payload: null };
  }
  const payload = await requestJSON(traceResourceLocateURL(traceID, item.kind || "", value), options);
  const anchor = payload?.anchor || payload?.items?.[0]?.anchor || payload?.items?.[0]?.node_id || "";
  return {
    href: conversationLocateHref(traceID, anchor, value),
    anchor,
    // The value is marked in the conversation only when it is small enough to
    // live in the address bar; the jump itself still works.
    highlight: value.length <= MAX_HIGHLIGHT_LENGTH ? value : "",
    payload,
  };
}
