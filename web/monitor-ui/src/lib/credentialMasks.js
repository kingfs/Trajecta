import { apiPaths, apiURL } from "./api.js";

/*
 * The pure half of the credential-masking read surfaces.
 *
 * The notifications in the event feed carry no credential bytes by design - one
 * fingerprint per rule, pushed to every open console - so the pair a reader
 * wants ("what did the client send, what left the machine") only exists in the
 * audit table behind `GET /api/secret-masks`. These helpers say which event
 * points at which page of that table and in what order to print the rows; the
 * components in `components/monitor/CredentialMasks.jsx` only render them.
 *
 * They live here rather than beside the component because `tests-unit` runs
 * plain `node --test` over `src/lib`, with no JSX transform.
 */

/** The parsed `details_json` of an event, or null when it is absent or not an object. */
export function parseEventDetails(event) {
  const value = event?.details_json;
  if (!value) {
    return null;
  }
  try {
    const parsed = typeof value === "string" ? JSON.parse(value) : value;
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * True for the secret-masking notification. The category is what the events
 * page filters on, so it decides; the source guards against a future detector
 * reusing the name.
 */
export function isCredentialMaskedEvent(event) {
  return event?.category === "credential_masked" && event?.source === "secretmask";
}

/**
 * The audit page an event points at: its trace, narrowed to its rule when the
 * details carry one. The rule narrows the page to the replacement the reader
 * clicked, because several rules can fire on one request.
 */
export function credentialMaskURLForEvent(event) {
  if (!isCredentialMaskedEvent(event)) {
    return null;
  }
  const details = parseEventDetails(event);
  // A trace-id search resolves the rule event to the trace the reader named.
  // The event row itself keeps the rule's newest trace, so the matched trace has
  // to win or the detail panel would answer with a different credential.
  const traceID = event?.matched_trace_id || event?.trace_id || details?.trace_id || "";
  if (!traceID) {
    return null;
  }
  const params = { trace_id: traceID, page_size: 50 };
  if (details?.rule) {
    params.rule = details.rule;
  }
  return apiURL(apiPaths.secretMasks, params);
}

/**
 * The action URL for one event row. A credential_masked read/resolve/ignore is
 * scoped to the trace the row is showing; the event id stays the rule-level
 * notification, so other traces are untouched. Every other event keeps the
 * existing rule-level path.
 */
export function scopedEventActionPath(event, path) {
  if (!path || !isCredentialMaskedEvent(event)) {
    return path;
  }
  const traceID = event?.matched_trace_id || event?.trace_id || "";
  if (!traceID) {
    return path;
  }
  return `${path}?trace_id=${encodeURIComponent(traceID)}`;
}

/** The URL the audit tab reads for one trace. */
export function credentialMaskURLForTrace(traceID) {
  return traceID ? apiURL(apiPaths.secretMasks, { trace_id: traceID, page_size: 50 }) : null;
}

/**
 * Sort the rows the way the exchange that wrote them ran: by event index, which
 * is the order the replacements appear in the request body. The table's own
 * ordering is by detection time and then by a random uuid, which shuffles the
 * replacements of a single request.
 */
export function sortCredentialMasks(items) {
  return [...(Array.isArray(items) ? items : [])].sort((a, b) => {
    const index = Number(a?.event_index || 0) - Number(b?.event_index || 0);
    if (index !== 0) {
      return index;
    }
    const start = Number(a?.start || 0) - Number(b?.start || 0);
    if (start !== 0) {
      return start;
    }
    return String(a?.rule || "").localeCompare(String(b?.rule || ""));
  });
}

/** Pull the rows out of a `GET /api/secret-masks` page. */
export function credentialMaskItems(data) {
  if (Array.isArray(data?.items)) {
    return data.items;
  }
  if (Array.isArray(data)) {
    return data;
  }
  return [];
}
