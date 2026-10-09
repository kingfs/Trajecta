import React, { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { WindowToggle } from "../components/common/Tabs";
import { DeleteIcon, DetailMetaPill, EditIcon, HomeIcon, InlineTag, ProbeIcon } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { SingleUsageCharts } from "../components/common/Charts";
import { Switch } from "../components/common/Controls";
import { useJSON } from "../hooks/useJSON";
import { useRefresh } from "../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, patchJSON, postJSON } from "../lib/api";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "../components/ui/dialog";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { buildTraceLink, formatCount, formatDateTime, formatDuration, formatTime, normalizeAnalyticsWindow, setOrDeleteParam } from "../lib/monitor";
import { buildPresetState, normalizePresetSelection, ProviderAdvancedFields } from "./ChannelsPage";

export function ProviderDetailPage() {
  const refresh = useRefresh();
  const { providerID = "", channelID = "" } = useParams();
  const effectiveProviderID = providerID || channelID;
  const navigate = useNavigate();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const [modelDraft, setModelDraft] = useState("");
  const [editOpen, setEditOpen] = useState(false);
  const [editForm, setEditForm] = useState(() => emptyEditForm());
  const [lastProbe, setLastProbe] = useState(null);
  const params = new URLSearchParams();
  params.set("window", windowValue);
  const detail = useJSON(apiURL(apiPaths.provider(effectiveProviderID), params), [effectiveProviderID, windowValue]);
  const presets = useJSON(apiPaths.providerPresets, []);
  const provider = detail.data || {};
  const summary = provider.summary || {};
  const modelsUsage = sortProviderModels(provider.models_usage || []);
  const discoveredDisabledModels = modelsUsage.filter((model) => model.source === "discovered" && !model.enabled).map((model) => model.model);
  const failures = provider.recent_failures || [];
  const probeRuns = provider.recent_probe_runs || [];
  const trends = provider.trends || [];

  useEffect(() => {
    if (!detail.data) {
      return;
    }
    setEditForm(editFormFromProvider(detail.data));
  }, [detail.data]);

  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    setSearchParams(next);
  };
  const reload = () => refresh();
  // The page keeps one busy slot for its toolbar, exactly as before, but each
  // action owns its pending state instead of sharing a string with eight others.
  // The action name is the mutation's own variable, so a command that names what
  // it operates on - a model toggle, a model delete - still reports which row it
  // is working on while it is in flight.
  const probe = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providerProbe(effectiveProviderID), { enable_discovered: false, detect_provider: true }),
    error: (err) => formatProbeActionError(err, t),
    onSuccess: (result) => {
      setLastProbe(result);
      reload();
    },
    onError: (err) => {
      // A failed probe can still carry a report worth showing.
      if (err.payload?.provider_probe) {
        setLastProbe(err.payload);
      }
      reload();
    },
  });

  const applyProbeSuggestions = useWriteMutation({
    mutationFn: (report) => patchJSON(apiPaths.provider(effectiveProviderID), providerProbeSuggestionPayload(provider, report)),
    error: "channelDetail.applyProbeError",
    onSuccess: () => {
      setLastProbe(null);
      reload();
    },
  });

  const setProviderEnabled = useWriteMutation({
    mutationFn: (enabled) => patchJSON(apiPaths.provider(effectiveProviderID), { enabled }),
    error: "channelDetail.updateProviderError",
    onSuccess: reload,
  });

  const saveProvider = useWriteMutation({
    mutationFn: () => patchJSON(apiPaths.provider(effectiveProviderID), providerPayloadFromForm(editForm)),
    error: "channelDetail.saveProviderError",
    onSuccess: () => {
      setEditOpen(false);
      reload();
    },
  });

  const setModelEnabled = useWriteMutation({
    mutationFn: ({ model, enabled }) => patchJSON(apiPaths.providerModel(effectiveProviderID, model), { enabled }),
    error: "channelDetail.updateModelError",
    onSuccess: reload,
  });

  const deleteProvider = useWriteMutation({
    mutationFn: () => deleteJSON(apiPaths.provider(effectiveProviderID)),
    error: "channelDetail.deleteProviderError",
    onSuccess: () => navigate("/providers"),
  });

  const deleteModel = useWriteMutation({
    mutationFn: ({ model }) => deleteJSON(apiPaths.providerModel(effectiveProviderID, model)),
    error: "channelDetail.deleteModelError",
    onSuccess: reload,
  });

  const addModel = useWriteMutation({
    mutationFn: ({ model }) => postJSON(apiPaths.providerModels(effectiveProviderID), { model, display_name: model, enabled: true }),
    error: "channelDetail.addModelError",
    onSuccess: () => {
      setModelDraft("");
      reload();
    },
  });

  const setModelsEnabled = useWriteMutation({
    mutationFn: ({ models, enabled }) => patchJSON(apiPaths.providerModelsBatch(effectiveProviderID), { models, enabled }),
    error: "channelDetail.updateModelsError",
    onSuccess: reload,
  });

  const busy =
    (probe.isPending && "probe") ||
    (applyProbeSuggestions.isPending && "apply-probe") ||
    (setProviderEnabled.isPending && "provider") ||
    (saveProvider.isPending && "save-provider") ||
    (setModelEnabled.isPending && setModelEnabled.variables?.model) ||
    (deleteProvider.isPending && "delete-provider") ||
    (deleteModel.isPending && `delete:${deleteModel.variables?.model}`) ||
    (addModel.isPending && "add-model") ||
    (setModelsEnabled.isPending && (setModelsEnabled.variables?.enabled ? "models-enable" : "models-disable")) ||
    "";

  const confirmDeleteProvider = () => {
    if (window.confirm(t("providers.deleteConfirm", { name: provider.name || effectiveProviderID }))) {
      deleteProvider.mutate();
    }
  };

  const confirmDeleteModel = (model) => {
    if (window.confirm(t("channelDetail.deleteModelConfirm", { model }))) {
      deleteModel.mutate({ model });
    }
  };

  const submitAddModel = (event) => {
    event.preventDefault();
    const model = modelDraft.trim();
    if (model) {
      addModel.mutate({ model });
    }
  };

  const applyProbe = () => {
    const report = lastProbe?.provider_probe;
    if (report) {
      applyProbeSuggestions.mutate(report);
    }
  };

  const toggleModels = (models, enabled) => {
    if (models.length) {
      setModelsEnabled.mutate({ models, enabled });
    }
  };

  return (
    <Dialog open={editOpen} onOpenChange={setEditOpen}>
      <div className="shell shell-detail">
        <header className="topbar detail-topbar">
          <div className="detail-title-block">
            <div className="detail-heading-row">
              <h1>{provider.name || effectiveProviderID}</h1>
              <div className="trace-tag-group detail-tag-group">
                <InlineTag tone={provider.enabled ? "green" : "default"}>{provider.enabled ? t("audit.enabled") : t("audit.disabled")}</InlineTag>
                <InlineTag tone={provider.source === "bootstrap" ? "gold" : "green"}>{providerSourceLabel(provider.source)}</InlineTag>
                <InlineTag tone="accent">{provider.provider_preset || "custom"}</InlineTag>
                {provider.secret_storage_mode ? <InlineTag tone={provider.secret_storage_mode === "plaintext-local" ? "gold" : "green"}>{provider.secret_storage_mode}</InlineTag> : null}
                {provider.last_probe_status ? <InlineTag tone={provider.last_probe_status === "success" ? "green" : "danger"}>{provider.last_probe_status}</InlineTag> : null}
              </div>
            </div>
            <div className="detail-meta-strip">
              <DetailMetaPill label={t("channelDetail.configSource")} value={providerSourceLabel(provider.source)} />
              <DetailMetaPill label={t("providers.apiType")} value={provider.api_type || "-"} />
              <DetailMetaPill label={t("channelDetail.mode")} value={provider.mode || "-"} />
              <DetailMetaPill label={t("channelDetail.baseUrl")} value={provider.base_url || "-"} mono />
              <DetailMetaPill label={t("channelDetail.models")} value={`${formatCount(provider.enabled_model_count)} / ${formatCount(provider.model_count)}`} />
              <DetailMetaPill label={t("common.requests")} value={formatCount(summary.request_count)} />
              <DetailMetaPill label={t("common.tokens")} value={formatCount(summary.total_tokens)} />
              {summary.missing_usage_request ? <DetailMetaPill label={t("channelDetail.missingUsage")} value={formatCount(summary.missing_usage_request)} /> : null}
            </div>
          </div>
          <div className="topbar-meta detail-toolbar">
            <div className="detail-toolbar-actions">
              <Link className="icon-button" to="/providers" title={t("channelDetail.backToProviders")} aria-label={t("channelDetail.backToProviders")}>
                <HomeIcon />
              </Link>
              <button className="icon-button" type="button" onClick={() => probe.mutate()} disabled={busy === "probe"} title={t("channelDetail.probeProvider")} aria-label={t("channelDetail.probeProvider")}><ProbeIcon /></button>
              <DialogTrigger asChild>
                <button className="icon-button" type="button" title={t("channelDetail.editProvider")} aria-label={t("channelDetail.editProvider")}><EditIcon /></button>
              </DialogTrigger>
              <button className="icon-button" type="button" onClick={confirmDeleteProvider} disabled={busy === "delete-provider"} title={t("providers.deleteTitle")} aria-label={t("providers.deleteTitle")}><DeleteIcon /></button>
              <Switch checked={Boolean(provider.enabled)} onChange={(enabled) => setProviderEnabled.mutate(enabled)} disabled={busy === "provider"} label={t("channelDetail.providerEnabled")} />
            </div>
            <span className="badge">{detail.data ? formatTime(detail.data.updated_at) : "..."}</span>
          </div>
        </header>

        <section className="panel">
          <div className="panel-head">
            <div>
              <p className="eyebrow">{t("channelDetail.analytics")}</p>
              <h2>{t("channelDetail.providerUsage")}</h2>
            </div>
            <div className="panel-head-actions">
              <WindowToggle value={windowValue} onChange={setWindow} label={t("channelDetail.windowLabel")} />
            </div>
          </div>
          <div className="hero-grid hero-grid-compact">
            <StatCard label={t("common.requests")} value={formatCount(summary.request_count)} />
            <StatCard label={t("common.errors")} value={formatCount(summary.failed_request)} accent={summary.failed_request ? "accent-red" : ""} />
            <StatCard label={t("common.tokens")} value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
            <StatCard label={t("common.success")} value={`${Number(summary.success_rate || 0).toFixed(1)}%`} />
          </div>
        </section>

        {detail.error ? <EmptyState title={t("channelDetail.loadError")} detail={detail.error} tone="danger" /> : null}
        {detail.loading && !detail.data ? <EmptyState title={t("channelDetail.loading")} detail={t("channelDetail.loadingDetail")} /> : null}
        {detail.data?.secret_storage_mode === "plaintext-local" ? (
          <EmptyState title={t("channelDetail.plaintextStorage")} detail={t("channelDetail.plaintextStorageDetail")} tone="danger" />
        ) : null}
        {lastProbe?.provider_probe ? (
          <ProviderProbeSuggestionPanel
            report={lastProbe.provider_probe}
            busy={busy === "apply-probe"}
            onApply={applyProbe}
          />
        ) : null}

        {detail.data && editOpen ? (
          <EditProviderDialog
            provider={provider}
            form={editForm}
            presetData={presets.data}
            saving={busy === "save-provider"}
            onChange={setEditForm}
            onReset={() => setEditForm(editFormFromProvider(provider))}
            onClose={() => setEditOpen(false)}
            onSave={saveProvider}
          />
        ) : null}

        {detail.data ? (
          <>
            <section className="panel">
              <div className="panel-head">
                <div>
                  <p className="eyebrow">{t("channelDetail.trend")}</p>
                  <h2>{t("channelDetail.tokenRequestBuckets")}</h2>
                </div>
              </div>
              <SingleUsageCharts items={trends} />
            </section>

            <section className="panel">
              <div className="panel-head">
                <div>
                  <p className="eyebrow">{t("nav.models")}</p>
                  <h2>{t("channelDetail.modelRoutingUsage")}</h2>
                  <p className="trace-subline">{t("channelDetail.enableHint")}</p>
                  {!provider.enabled ? <p className="trace-subline">{t("channelDetail.providerDisabledHint")}</p> : null}
                </div>
              </div>
              <form className="filter-bar" onSubmit={submitAddModel}>
                <input className="filter-input filter-input-wide" type="search" value={modelDraft} onChange={(event) => setModelDraft(event.target.value)} placeholder={t("channelDetail.addModelPlaceholder")} />
                <button className="ghost-button active" type="submit" disabled={busy === "add-model"}>{busy === "add-model" ? t("channelDetail.adding") : t("channelDetail.addModel")}</button>
                <button className="ghost-button" type="button" onClick={() => toggleModels(discoveredDisabledModels, true)} disabled={!discoveredDisabledModels.length || busy === "models-enable"}>{busy === "models-enable" ? t("channelDetail.enabling") : t("channelDetail.enableDiscovered", { count: formatCount(discoveredDisabledModels.length) })}</button>
              </form>
              <div className="provider-model-card-grid">
                {modelsUsage.length ? modelsUsage.map((model) => (
                  <ProviderModelRow
                    key={model.model}
                    item={model}
                    providerEnabled={Boolean(provider.enabled)}
                    busy={busy === model.model}
                    deleting={busy === `delete:${model.model}`}
                    onToggle={() => setModelEnabled.mutate({ model: model.model, enabled: !model.enabled })}
                    onDelete={() => confirmDeleteModel(model.model)}
                  />
                )) : <EmptyState title={t("channelDetail.noModels")} detail={t("channelDetail.noModelsDetail")} compact />}
              </div>
            </section>

            <section className="panel">
              <div className="panel-head">
                <div>
                  <p className="eyebrow">{t("channelDetail.discovery")}</p>
                  <h2>{t("channelDetail.recentProbes")}</h2>
                </div>
              </div>
              {probeRuns.length ? (
                <div className="provider-probe-list">
                  {probeRuns.map((run) => <ProbeRunCard key={run.id} item={run} />)}
                </div>
              ) : (
                <EmptyState title={t("channelDetail.noProbeRuns")} detail={t("channelDetail.noProbeRunsDetail")} />
              )}
            </section>

            <section className="panel">
              <div className="panel-head">
                <div>
                  <p className="eyebrow">{t("channelDetail.failures")}</p>
                  <h2>{t("channelDetail.recentFailedTraces")}</h2>
                </div>
              </div>
              {failures.length ? (
                <div className="upstream-failure-list upstream-failure-list-detail">
                  {failures.map((failure) => (
                    <Link key={failure.trace_id} className="upstream-failure-card" to={buildTraceLink(failure.trace_id, "providers", "", "", "failure")}>
                      <div className="trace-tag-group">
                        <InlineTag tone="danger">{failure.status_code}</InlineTag>
                        {failure.reason ? <InlineTag>{failure.reason}</InlineTag> : null}
                      </div>
                      <strong>{failure.model || t("channelDetail.unknownModel")}</strong>
                      <span>{formatDateTime(failure.recorded_at)}</span>
                      {failure.error_text ? <div className="upstream-failure-detail">{failure.error_text}</div> : null}
                    </Link>
                  ))}
                </div>
              ) : (
                <EmptyState title={t("channelDetail.noRecentFailures")} detail={t("channelDetail.noRecentFailuresDetail")} />
              )}
            </section>
          </>
        ) : null}
      </div>
    </Dialog>
  );
}

function EditProviderDialog({ provider, form, presetData, saving, onChange, onReset, onClose, onSave }) {
  const { t } = useI18n();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const presetState = buildPresetState(presetData, form.provider_preset, form.routing_profile);
  const updateForm = (key, value) => {
    onChange((current) => normalizePresetSelection({ ...current, [key]: value }, presetData, key));
  };
  const submit = async (event) => {
    event.preventDefault();
    await onSave();
  };

  return (
    <DialogContent className="provider-edit-modal">
      <form onSubmit={submit}>
        <DialogHeader>
          <div>
            <p className="eyebrow">{t("providers.configuration")}</p>
            <DialogTitle>{t("channelDetail.editProvider")}</DialogTitle>
          </div>
          <DialogClose asChild>
            <button className="icon-button" type="button" aria-label={t("common.close")}>x</button>
          </DialogClose>
        </DialogHeader>
        <div className="provider-form provider-form-modal">
          <label>{t("providers.name")}<input required value={form.name} onChange={(event) => updateForm("name", event.target.value)} /></label>
          <label>{t("providers.preset")}<select value={form.provider_preset} onChange={(event) => updateForm("provider_preset", event.target.value)}>{presetState.options.map((preset) => <option key={preset} value={preset}>{preset}</option>)}</select></label>
          <label className="provider-form-wide">{t("providers.baseURL")}<input required value={form.base_url} onChange={(event) => updateForm("base_url", event.target.value)} /></label>
          <label className="provider-form-wide">{t("providers.apiKey")}<input type="password" value={form.api_key} onChange={(event) => updateForm("api_key", event.target.value)} placeholder={provider.api_key_hint ? t("channelDetail.keepApiKey", { hint: provider.api_key_hint }) : t("channelDetail.unchanged")} /></label>
          <label className="provider-form-check provider-form-wide"><input type="checkbox" checked={form.allow_unknown_models} onChange={(event) => updateForm("allow_unknown_models", event.target.checked)} /> {t("providers.allowUnknown")}</label>
        </div>
        <button className="ghost-button" type="button" onClick={() => setAdvancedOpen((open) => !open)}>{advancedOpen ? t("providers.hideAdvanced") : t("providers.advanced")}</button>
        {advancedOpen ? (
          <div className="provider-form provider-form-modal">
            <ProviderAdvancedFields form={form} presetState={presetState} onChange={updateForm} includeHeaders />
          </div>
        ) : null}
        <DialogFooter>
          <button className="ghost-button" type="button" onClick={onReset}>{t("common.reset")}</button>
          <button className="ghost-button" type="button" onClick={onClose}>{t("providers.cancel")}</button>
          <button className="ghost-button active" type="submit" disabled={saving}>{saving ? t("common.saving") : t("channelDetail.saveChanges")}</button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}

function ProbeRunCard({ item }) {
  const { t } = useI18n();
  const failed = item.status !== "success";
  return (
    <div className={failed ? "provider-probe-card provider-probe-card-failed" : "provider-probe-card"}>
      <div className="provider-probe-card-head">
        <div className="trace-tag-group">
          <InlineTag tone={failed ? "danger" : "green"}>{item.status || "unknown"}</InlineTag>
          {item.failure_reason ? <InlineTag tone="accent">{item.failure_reason}</InlineTag> : null}
          {item.status_code ? <InlineTag>{item.status_code}</InlineTag> : null}
        </div>
        <span>{formatDateTime(item.completed_at || item.started_at)}</span>
      </div>
      <div className="provider-probe-meta">
        <span>{t("channelDetail.discoveredCount", { count: formatCount(item.discovered_count) })}</span>
        <span>{t("channelDetail.enabledCount", { count: formatCount(item.enabled_count) })}</span>
        <span>{formatDuration(item.duration_ms)}</span>
      </div>
      {item.endpoint ? <div className="provider-probe-endpoint">{item.endpoint}</div> : null}
      {item.error_text ? <div className="upstream-failure-detail">{item.error_text}</div> : null}
      {item.retry_hint ? <div className="provider-probe-hint">{item.retry_hint}</div> : null}
    </div>
  );
}

function ProviderProbeSuggestionPanel({ report, busy, onApply }) {
  const { t } = useI18n();
  const capabilities = Array.isArray(report.capabilities) ? report.capabilities : [];
  const warnings = Array.isArray(report.warnings) ? report.warnings : [];
  return (
    <section className="panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("providers.detection")}</p>
          <h2>{t("providers.probeSuggestions")}</h2>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone={report.status === "detected" ? "green" : report.status === "error" ? "danger" : "gold"}>{report.status || "unknown"}</InlineTag>
          {report.confidence ? <InlineTag tone="accent">{Math.round(Number(report.confidence) * 100)}%</InlineTag> : null}
        </div>
      </div>
      <div className="detail-meta-strip">
        <Metric label={t("providers.apiType")} value={report.suggested_api_type || "-"} />
        <Metric label={t("providers.protocol")} value={report.suggested_protocol_family || "-"} />
        <Metric label={t("providers.capabilities")} value={capabilities.length ? capabilities.join(", ") : "-"} />
      </div>
      {warnings.length ? <p className="trace-subline">{warnings.join(" · ")}</p> : null}
      <div className="provider-form-actions">
        <button className="ghost-button active" type="button" onClick={onApply} disabled={busy || report.status !== "detected"}>{busy ? t("providers.applying") : t("providers.applySuggestions")}</button>
      </div>
    </section>
  );
}

function ProviderModelRow({ item, providerEnabled, busy, deleting, onToggle, onDelete }) {
  const { t } = useI18n();
  const summary = item.summary || {};
  const isDiscoveredDisabled = item.source === "discovered" && !item.enabled;
  const canDelete = item.source !== "trace";
  return (
    <div className="provider-model-card">
      <div className="provider-model-card-head">
        <div>
          <strong>{item.model}</strong>
          <span>{isDiscoveredDisabled ? t("channelDetail.discoveredDisabled") : modelSourceLabel(item.source)}</span>
        </div>
        <div className="action-group">
          {item.source !== "trace" ? <Switch checked={Boolean(item.enabled)} onChange={onToggle} disabled={busy} label={t("channelDetail.modelEnabled", { model: item.model })} /> : <span>{t("channelDetail.historyOnly")}</span>}
          {canDelete ? (
            <button className="icon-button" type="button" onClick={onDelete} disabled={deleting} title={t("channelDetail.deleteModel")} aria-label={t("channelDetail.deleteModelAria", { model: item.model })}>
              <DeleteIcon />
            </button>
          ) : null}
        </div>
      </div>
      <div className="trace-tag-group">
        <InlineTag tone={item.enabled ? "green" : "default"}>{item.source === "trace" ? t("channelDetail.historyOnlyTag") : item.enabled ? t("audit.enabled") : t("audit.disabled")}</InlineTag>
        {item.enabled && !providerEnabled ? <InlineTag tone="gold">{t("channelDetail.blockedProviderDisabled")}</InlineTag> : null}
      </div>
      <div className="model-market-metrics model-market-metrics-compact">
        <Metric label="req" value={formatCount(summary.request_count)} />
        <Metric label="err" value={formatCount(summary.failed_request)} danger={Number(summary.failed_request || 0) > 0} />
        <Metric label="tok" value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
      </div>
    </div>
  );
}

function sortProviderModels(items) {
  return items.slice().sort((left, right) => {
    if (Boolean(left.enabled) !== Boolean(right.enabled)) {
      return left.enabled ? -1 : 1;
    }
    const leftRequests = Number(left.summary?.request_count || 0);
    const rightRequests = Number(right.summary?.request_count || 0);
    if (leftRequests !== rightRequests) {
      return rightRequests - leftRequests;
    }
    return String(left.model || "").localeCompare(String(right.model || ""));
  });
}

function providerSourceLabel(source) {
  switch (source) {
    case "bootstrap":
      return "bootstrap";
    case "manual":
    case "":
    case undefined:
      return "web-managed";
    default:
      return source;
  }
}

function modelSourceLabel(source) {
  switch (source) {
    case "manual":
      return "manual";
    case "static":
      return "bootstrap static";
    case "discovered":
      return "probe discovered";
    case "trace":
      return "seen in trace";
    default:
      return source || "unknown";
  }
}

function Metric({ label, value, detail = "", danger = false }) {
  return (
    <span className={danger ? "model-market-metric model-market-metric-danger" : "model-market-metric"}>
      <span>{label}</span>
      <strong>{value}</strong>
      {detail ? <small>{detail}</small> : null}
    </span>
  );
}

function usageCoverageDetail(missing, t = (key, values) => `${values?.count || 0} missing usage`) {
  const count = Number(missing || 0);
  return count > 0 ? t("providers.missingUsage", { count: formatCount(count) }) : "";
}

function formatProbeActionError(err, t = (key) => key) {
  const payload = err?.payload || {};
  const parts = [];
  if (payload.failure_reason) {
    parts.push(payload.failure_reason);
  }
  if (payload.error_text || err?.message) {
    parts.push(payload.error_text || err.message);
  }
  if (payload.retry_hint) {
    parts.push(payload.retry_hint);
  }
  return parts.join(" · ") || t("channelDetail.probeFailed");
}

function emptyEditForm() {
  return {
    name: "",
    base_url: "",
    provider_preset: "",
    api_type: "chat_completions",
    mode: "proxy",
    capabilities: {},
    protocol_family: "",
    routing_profile: "",
    api_version: "",
    deployment: "",
    project: "",
    location: "",
    model_resource: "",
    api_key: "",
    priority: 0,
    weight: 1,
    capacity_hint: 1,
    model_discovery: "list_models",
    allow_unknown_models: false,
    headers_text: "",
  };
}

function editFormFromProvider(provider = {}) {
  const headers = provider.headers || {};
  return {
    name: provider.name || "",
    base_url: provider.base_url || "",
    provider_preset: provider.provider_preset || "",
    api_type: provider.api_type || "chat_completions",
    mode: provider.mode || "proxy",
    capabilities: provider.capabilities || {},
    protocol_family: provider.protocol_family || "",
    routing_profile: provider.routing_profile || "",
    api_version: provider.api_version || "",
    deployment: provider.deployment || "",
    project: provider.project || "",
    location: provider.location || "",
    model_resource: provider.model_resource || "",
    api_key: "",
    priority: provider.priority ?? 0,
    weight: provider.weight ?? 1,
    capacity_hint: provider.capacity_hint ?? 1,
    model_discovery: provider.model_discovery || "list_models",
    allow_unknown_models: Boolean(provider.allow_unknown_models),
    headers_text: Object.keys(headers).sort().map((key) => `${key}: ${headers[key]}`).join("\n"),
  };
}

function providerPayloadFromForm(form) {
  const payload = {
    name: form.name,
    base_url: form.base_url,
    provider_preset: form.provider_preset,
    api_type: form.api_type,
    mode: form.mode,
    capabilities: normalizeCapabilities(form.capabilities),
    protocol_family: form.protocol_family,
    routing_profile: form.routing_profile,
    api_version: form.api_version,
    deployment: form.deployment,
    project: form.project,
    location: form.location,
    model_resource: form.model_resource,
    priority: Number(form.priority || 0),
    weight: Number(form.weight || 1),
    capacity_hint: Number(form.capacity_hint || 1),
    model_discovery: form.model_discovery,
    allow_unknown_models: Boolean(form.allow_unknown_models),
    headers: parseHeadersText(form.headers_text),
  };
  if (form.api_key.trim()) {
    payload.api_key = form.api_key.trim();
  }
  return payload;
}

function providerProbeSuggestionPayload(provider = {}, report = {}) {
  const payload = {};
  if (report.suggested_api_type) {
    payload.api_type = report.suggested_api_type;
  }
  if (report.suggested_protocol_family) {
    payload.protocol_family = report.suggested_protocol_family;
  }
  const capabilities = { ...(provider.capabilities || {}) };
  for (const capability of report.capabilities || []) {
    switch (capability) {
      case "responses":
        capabilities.responses = true;
        break;
      case "chat_completions":
        capabilities.chat_completions = true;
        break;
      case "tool_calling":
        capabilities.tool_calling = true;
        break;
      case "models":
        capabilities.models = true;
        break;
      case "embeddings":
        capabilities.embeddings = true;
        break;
      case "tokenize":
        capabilities.tokenize = true;
        break;
      default:
        break;
    }
  }
  payload.capabilities = normalizeCapabilities(capabilities);
  return payload;
}

function normalizeCapabilities(value) {
  const capabilities = {};
  for (const key of ["responses", "chat_completions", "tool_calling", "models", "embeddings", "tokenize"]) {
    if (typeof value?.[key] === "boolean") {
      capabilities[key] = value[key];
    }
  }
  return capabilities;
}

function parseHeadersText(value) {
  const headers = {};
  String(value || "").split(/\r?\n/).forEach((line) => {
    const trimmed = line.trim();
    if (!trimmed) {
      return;
    }
    const index = trimmed.indexOf(":");
    if (index <= 0) {
      return;
    }
    const key = trimmed.slice(0, index).trim();
    const headerValue = trimmed.slice(index + 1).trim();
    if (!key) {
      return;
    }
    headers[key] = headerValue === "***" ? { keep: true } : headerValue;
  });
  return headers;
}
