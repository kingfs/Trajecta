import { fetchEventSource } from "@microsoft/fetch-event-source";

export const MONITOR_TOKEN_KEY = "trajecta.monitor.token";

export const apiPaths = {
  authStatus: "/api/auth/status",
  authCheck: "/api/auth/check",
  authLogin: "/api/auth/login",
  authMe: "/api/auth/me",
  authPassword: "/api/auth/password",
  authTokens: "/api/auth/tokens",
  overview: "/api/overview",
  systemRuntime: "/api/system/runtime",
  systemHost: "/api/system/host",
  systemDatabase: "/api/system/db",
  systemSlowQueries: "/api/system/slow-queries",
  events: "/api/events",
  eventsSummary: "/api/events/summary",
  eventsStream: "/api/events/stream",
  eventsReadAll: "/api/events/read-all",
  eventRead: (eventID) => `/api/events/${encodeURIComponent(eventID)}/read`,
  eventResolve: (eventID) => `/api/events/${encodeURIComponent(eventID)}/resolve`,
  eventIgnore: (eventID) => `/api/events/${encodeURIComponent(eventID)}/ignore`,
  traces: "/api/traces",
  findings: "/api/findings",
  responsesAuditTrace: "/api/responses/audit/trace",
  responsesFunctionExecutors: "/api/responses/function-executors",
  analysis: "/api/analysis",
  analysisJobs: "/api/analysis/jobs",
  analysisBatchReanalyze: "/api/analysis/batch/reanalyze",
  trace: (traceID) => `/api/traces/${encodeURIComponent(traceID)}`,
  traceRaw: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/raw`,
  traceObservation: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/observation`,
  traceFindings: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/findings`,
  tracePerformance: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/performance`,
  traceDownload: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/download`,
  traceRepairUsage: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/repair-usage`,
  traceReanalyze: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/reanalyze`,
  sessions: "/api/sessions",
  session: (sessionID) => `/api/sessions/${encodeURIComponent(sessionID)}`,
  // The trajectory endpoint caps the default view (about 500 traces) and can
  // stream the complete session as NDJSON instead of buffering it.
  sessionTrajectory: (sessionID, { full = false, stream = false } = {}) => {
    const params = new URLSearchParams();
    if (full) params.set("full", "1");
    if (stream) params.set("stream", "1");
    const query = params.toString();
    return `/api/sessions/${encodeURIComponent(sessionID)}/trajectory${query ? `?${query}` : ""}`;
  },
  sessionReanalyze: (sessionID) => `/api/sessions/${encodeURIComponent(sessionID)}/reanalyze`,
  models: "/api/models",
  model: (model) => `/api/models/${encodeURIComponent(model)}`,
  modelSpecLookup: (model) => `/api/models/${encodeURIComponent(model)}/spec-lookup`,
  providers: "/api/channels",
  provider: (providerID) => `/api/channels/${encodeURIComponent(providerID)}`,
  providerProbe: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/probe`,
  providerModels: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/models`,
  providerModelsBatch: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/models/batch`,
  providerModel: (providerID, model) => `/api/channels/${encodeURIComponent(providerID)}/models/${encodeURIComponent(model)}`,
  providerProbePreview: "/api/provider-probe",
  providerProbeReport: "/api/provider-probe/report",
  providerProbeApply: "/api/provider-probe/report/apply",
  providerSetupValidate: "/api/provider-setup/validate",
  providerSetupApply: "/api/provider-setup/apply",
  providerPresets: "/api/provider-presets",
  routingExchanges: "/api/routing/exchanges",
  routingSummary: "/api/routing/summary",
  routingInspect: "/api/routing/inspect",
  routingSettings: "/api/settings/routing",
  modelAliases: "/api/model-aliases",
  modelAliasValidate: "/api/model-aliases/validate",
  modelAlias: (aliasID) => `/api/model-aliases/${encodeURIComponent(aliasID)}`,
  upstream: (upstreamID) => `/api/upstreams/${encodeURIComponent(upstreamID)}`,
};

export function monitorAuthHeaders() {
  const token = window.localStorage.getItem(MONITOR_TOKEN_KEY) || "";
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function apiURL(path, params = null) {
  const query = params instanceof URLSearchParams ? params.toString() : new URLSearchParams(params || {}).toString();
  return query ? `${path}?${query}` : path;
}

/**
 * @param {string} path
 * @param {{ method?: string, headers?: Record<string, string>, body?: BodyInit | null, signal?: AbortSignal }} [options]
 * @returns {Promise<any>}
 */
export async function requestJSON(path, { method = "GET", headers = {}, body, signal } = {}) {
  const requestHeaders = {
    ...monitorAuthHeaders(),
    ...headers,
  };
  const response = await fetch(path, {
    method,
    headers: requestHeaders,
    body,
    signal,
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const err = new Error(payload.error || payload.error_text || `request failed: ${response.status}`);
    err.payload = payload;
    err.status = response.status;
    throw err;
  }
  return payload;
}

export function postJSON(path, payload, options = {}) {
  return requestJSON(path, {
    ...options,
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
    body: JSON.stringify(payload),
  });
}

export function patchJSON(path, payload, options = {}) {
  return requestJSON(path, {
    ...options,
    method: "PATCH",
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
    body: JSON.stringify(payload),
  });
}

export function deleteJSON(path, options = {}) {
  return requestJSON(path, {
    ...options,
    method: "DELETE",
  });
}

export async function downloadBlob(path) {
  const response = await fetch(path, { headers: monitorAuthHeaders() });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error || `request failed: ${response.status}`);
  }
  return response.blob();
}

export function listItems(payload) {
  return Array.isArray(payload?.items) ? payload.items : [];
}

// streamSystemEvents subscribes to the monitor SSE stream. EventSource cannot
// send an Authorization header, which used to force the JWT into the query
// string (?access_token=...) where reverse proxies log it. `fetchEventSource`
// exists for exactly this case - SSE over fetch, so the token travels in a
// header only - and owns the frame parser, the reconnect and the cancellation
// that this function used to write by hand.
export function streamSystemEvents(handlers = {}) {
  const controller = new AbortController();
  const { onEvent, onError } = handlers;
  let reconnectTimer = 0;

  const connect = () => {
    fetchEventSource(apiPaths.eventsStream, {
      headers: { ...monitorAuthHeaders(), Accept: "text/event-stream" },
      signal: controller.signal,
      // The connection is kept while the tab is hidden, which is what the
      // hand-written reader did: it never watched document visibility.
      openWhenHidden: true,
      async onopen(response) {
        if (!response.ok) {
          throw new Error(`event stream failed: ${response.status}`);
        }
        const contentType = response.headers.get("content-type") || "";
        if (!contentType.startsWith("text/event-stream")) {
          throw new Error(`event stream returned ${contentType || "no content type"}`);
        }
      },
      onmessage(event) {
        // The parser dispatches on every blank line, heartbeat comments
        // included, so a block with no `data:` field arrives here as an empty
        // payload. The hand-written reader dropped those and so does this.
        if (typeof onEvent !== "function" || event.data === "") {
          return;
        }
        onEvent({ event: event.event || "", data: event.data });
      },
      onerror(err) {
        if (typeof onError === "function") {
          onError(err);
        }
        // The library retries on this interval rather than rejecting, which is
        // the reconnect the hand-written reader did itself.
        return 5000;
      },
      onclose() {
        // A clean close ends the subscription as far as the library is
        // concerned, so the reconnect is ours. Without it the event badge would
        // quietly stop updating the first time the server ended the stream.
        if (!controller.signal.aborted) {
          reconnectTimer = window.setTimeout(connect, 5000);
        }
      },
    }).catch(() => {
      // onerror owns reporting; the promise only rejects when the handler
      // itself throws, and an unhandled rejection there helps nobody.
    });
  };
  connect();

  return () => {
    controller.abort();
    window.clearTimeout(reconnectTimer);
  };
}
