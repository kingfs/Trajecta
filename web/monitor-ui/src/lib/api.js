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
// string (?access_token=...) where reverse proxies log it. This reader uses
// fetch + ReadableStream instead, so the token travels in a header only.
export function streamSystemEvents(handlers = {}) {
  const controller = new AbortController();
  const { onEvent, onError } = handlers;
  let stopped = false;

  const dispatch = (rawEvent) => {
    if (typeof onEvent !== "function") {
      return;
    }
    let eventName = "";
    const dataLines = [];
    for (const line of rawEvent.split("\n")) {
      if (line.startsWith("event:")) {
        eventName = line.slice("event:".length).trim();
      } else if (line.startsWith("data:")) {
        dataLines.push(line.slice("data:".length).replace(/^ /, ""));
      }
    }
    if (dataLines.length > 0) {
      onEvent({ event: eventName, data: dataLines.join("\n") });
    }
  };

  const read = async () => {
    const response = await fetch(apiPaths.eventsStream, {
      headers: { ...monitorAuthHeaders(), Accept: "text/event-stream" },
      signal: controller.signal,
    });
    if (!response.ok || !response.body) {
      throw new Error(`event stream failed: ${response.status}`);
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      let separator = buffer.indexOf("\n\n");
      while (separator >= 0) {
        const rawEvent = buffer.slice(0, separator);
        buffer = buffer.slice(separator + 2);
        if (rawEvent.trim() !== "") {
          dispatch(rawEvent);
        }
        separator = buffer.indexOf("\n\n");
      }
    }
  };

  const start = () => {
    if (stopped) {
      return;
    }
    read()
      .catch((err) => {
        if (stopped || controller.signal.aborted) {
          return;
        }
        if (typeof onError === "function") {
          onError(err);
        }
      })
      .then(() => {
        if (!stopped) {
          setTimeout(start, 5000);
        }
      });
  };
  start();

  return () => {
    stopped = true;
    controller.abort();
  };
}
