

export const apiPaths = {
  authStatus: "/api/auth/status",
  authCheck: "/api/auth/check",
  authLogin: "/api/auth/login",
  authLogout: "/api/auth/logout",
  authMe: "/api/auth/me",
  authPassword: "/api/auth/password",
  authTokens: "/api/auth/tokens",
  overview: "/api/overview",
  // The build identity and the About page's two switches. Readable by every
  // signed-in role, unlike the /api/system/* reads below.
  about: "/api/about",
  versionLatest: "/api/version/latest",
  systemRuntime: "/api/system/runtime",
  systemHost: "/api/system/host",
  systemDatabase: "/api/system/db",
  systemSlowQueries: "/api/system/slow-queries",
  events: "/api/events",
  eventsSummary: "/api/events/summary",
  eventsSocket: "/api/events/ws",
  eventsReadAll: "/api/events/read-all",
  eventRead: (eventID) => `/api/events/${encodeURIComponent(eventID)}/read`,
  eventResolve: (eventID) => `/api/events/${encodeURIComponent(eventID)}/resolve`,
  eventIgnore: (eventID) => `/api/events/${encodeURIComponent(eventID)}/ignore`,
  traces: "/api/traces",
  findings: "/api/findings",
  // The credential-masking audit table. It is the only read that returns the
  // masked value next to the stand-in that replaced it; the event feed
  // deliberately carries neither.
  secretMasks: "/api/secret-masks",
  // The global real-to-stand-in mapping file. Separate from the per-trace audit
  // above: these pairs are not tied to one recording, and a row can be deleted
  // while the proxy runs.
  secretMappings: "/api/secret-mappings",
  secretMapping: (seq) => `/api/secret-mappings/${encodeURIComponent(seq)}`,
  // The false-positive list consulted before a replacement is rendered. Unlike
  // a mapping delete, an entry here stops the rule from masking the value.
  secretSuppressions: "/api/secret-suppressions",
  secretSuppression: (id) => `/api/secret-suppressions/${encodeURIComponent(id)}`,
  responsesAuditTrace: "/api/responses/audit/trace",
  responsesFunctionExecutors: "/api/responses/function-executors",
  analysis: "/api/analysis",
  analysisJobs: "/api/analysis/jobs",
  analysisBatchReanalyze: "/api/analysis/batch/reanalyze",
  trace: (traceID) => `/api/traces/${encodeURIComponent(traceID)}`,
  traceRaw: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/raw`,
  traceObservation: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/observation`,
  traceFindings: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/findings`,
  traceResources: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/resources`,
  traceResourceLocate: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/resources/locate`,
  traceFindingLocate: (traceID, findingID) =>
    `/api/traces/${encodeURIComponent(traceID)}/findings/${encodeURIComponent(findingID)}/locate`,
  traceMaskLocate: (traceID, maskID) =>
    `/api/traces/${encodeURIComponent(traceID)}/secret-masks/${encodeURIComponent(maskID)}/locate`,
  tracePerformance: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/performance`,
  traceDownload: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/download`,
  traceRepairUsage: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/repair-usage`,
  traceReparse: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/reparse`,
  traceReanalyze: (traceID) => `/api/traces/${encodeURIComponent(traceID)}/reanalyze`,
  sessions: "/api/sessions",
  // One row per project directory a harness declared in its prompt, with the
  // sessions attributed to it. `project` on /api/sessions filters by root_key.
  projects: "/api/projects",
  projectsBackfill: "/api/projects/backfill",
  // Detection health: declared vs rejected declarations, why they were
  // rejected, and which harness they came from. The drift alarm.
  projectsDetection: "/api/projects/detection",
  // Operator merges: one raw path counted as another project. Read-time only.
  projectAliases: "/api/project-aliases",
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
  // The step model behind the trace page's 轨迹 and 统计 tabs. It is derived on
  // demand from the index rows plus the cassettes; nothing is persisted, so the
  // only parameter that matters is how many steps the window returns.
  traceSessionSteps: (traceID, { limit = 0, before = "", after = "" } = {}) => {
    const params = new URLSearchParams();
    if (limit > 0) params.set("limit", String(limit));
    if (before) params.set("before", before);
    if (after) params.set("after", after);
    const query = params.toString();
    return `/api/traces/${encodeURIComponent(traceID)}/session-steps${query ? `?${query}` : ""}`;
  },
  sessionSteps: (sessionID, { limit = 0, before = "", after = "" } = {}) => {
    const params = new URLSearchParams();
    if (limit > 0) params.set("limit", String(limit));
    if (before) params.set("before", before);
    if (after) params.set("after", after);
    const query = params.toString();
    return `/api/sessions/${encodeURIComponent(sessionID)}/steps${query ? `?${query}` : ""}`;
  },
  sessionReanalyze: (sessionID) => `/api/sessions/${encodeURIComponent(sessionID)}/reanalyze`,
  models: "/api/models",
  // The configuration-facing catalogue: every model an enabled channel
  // exposes. /api/models above remains the traffic-facing list.
  modelSquare: "/api/model-square",
  model: (model) => `/api/models/${encodeURIComponent(model)}`,
  modelSpecLookup: (model) => `/api/models/${encodeURIComponent(model)}/spec-lookup`,
  providers: "/api/channels",
  provider: (providerID) => `/api/channels/${encodeURIComponent(providerID)}`,
  providerProbe: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/probe`,
  providerModels: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/models`,
  providerModelsBatch: (providerID) => `/api/channels/${encodeURIComponent(providerID)}/models/batch`,
  providerModel: (providerID, model) => `/api/channels/${encodeURIComponent(providerID)}/models/${encodeURIComponent(model)}`,
  // The per-model quick connectivity test: one POST probes every protocol the
  // channel declares for the given model and returns the per-protocol outcome.
  providerModelTest: "/api/provider-model-test",
  // Interface auto-detection: probes the model's protocol surfaces and writes
  // the ``channel_models`` supports_* columns. With only ``channel_id`` it
  // detects every enabled model of that channel.
  providerModelDetect: "/api/provider-model-detect",
  providerProbePreview: "/api/provider-probe",
  providerProbeReport: "/api/provider-probe/report",
  providerProbeApply: "/api/provider-probe/report/apply",
  providerPresets: "/api/provider-presets",
  providerCatalog: "/api/catalog/providers",
  // Channel plugins: the vendor catalog units gated by the plugins page.
  plugins: "/api/plugins",
  plugin: (pluginID) => `/api/plugins/${encodeURIComponent(pluginID)}`,
  // Detection plugins: the algorithm catalog behind the plugins page's
  // detection tab. Same enablement pattern as the channel plugins above; the
  // PATCH also carries opaque per-plugin settings and answers 409
  // plugin_in_use when a hot-lane plugin still has stand-in mappings.
  detectorPlugins: "/api/detector-plugins",
  detectorPlugin: (pluginID) => `/api/detector-plugins/${encodeURIComponent(pluginID)}`,
  // Detection observability: hot-path counters, the 30-day per-rule replacement
  // tally and the suppression/mapping sizes behind the detection tab's stats
  // section. Admin-only read.
  detectorPluginStats: "/api/detector-plugins/stats",
  // The read-only rule sandbox: POST a body, get every finding's rule, span,
  // confidence, suppression decision and a format-preserving stand-in rendered
  // by a throwaway in-memory vault. It never writes the durable mapping store.
  detectorPluginSecretmaskTest: "/api/detector-plugins/secretmask/test",
  // The static logical structure of the Exchange IR behind 质量 → XIR 结构.
  // Metadata only: no traffic and no store reads; the plugin overlay comes
  // from the detector-plugins catalog above.
  xirStructure: "/api/xir/structure",
  routingInspect: "/api/routing/inspect",
  routingSettings: "/api/settings/routing",
  networkProxySettings: "/api/settings/network-proxy",
  // The two parse lanes (fresh traffic and the historical sweep) and their live
  // depth. The reset only clears the sweep's cursor; it writes no derived data.
  settingsAnalysisQueue: "/api/settings/analysis-queue",
  analysisQueueResetBackfill: "/api/settings/analysis-queue/reset-backfill",
  ratesAlertsSettings: "/api/settings/rates-alerts",
  modelAliases: "/api/model-aliases",
  modelAliasValidate: "/api/model-aliases/validate",
  modelAlias: (aliasID) => `/api/model-aliases/${encodeURIComponent(aliasID)}`,
  upstream: (upstreamID) => `/api/upstreams/${encodeURIComponent(upstreamID)}`,
};

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
    ...headers,
  };
  const response = await fetch(path, {
    credentials: "same-origin",
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
  const response = await fetch(path, { credentials: "same-origin" });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error || `request failed: ${response.status}`);
  }
  return response.blob();
}

