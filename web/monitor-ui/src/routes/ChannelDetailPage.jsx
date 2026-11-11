import React, { useEffect, useState } from "react";
import { Card } from "../components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Input } from "../components/ui/input";
import { Button } from "../components/ui/button";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { WindowToggle } from "../components/common/Tabs";
import { DeleteIcon, DetailMetaPill, DetectIcon, EditIcon, HomeIcon, InlineTag, ProbeIcon, TestIcon } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { SingleUsageCharts } from "../components/common/Charts";
import { Switch } from "../components/common/Controls";
import { useJSON } from "../hooks/useJSON";
import { useRefresh } from "../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, patchJSON, postJSON } from "../lib/api";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "../components/ui/dialog";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { buildTraceLink, formatCount, formatDateTime, formatDuration, formatRateAvg, normalizeAnalyticsWindow, setOrDeleteParam } from "../lib/monitor";
import { buildPresetState, capabilitiesFromInterfaces, deriveCustomChannelRouting, INTERFACE_OPTIONS, interfacesFromCapabilities, normalizePresetSelection, ProviderAdvancedFields } from "./ChannelsPage";
import {
  applyResponsesDisabled,
  buildModelTestPayload,
  disableResponsesPayload,
  disableResponsesSuggestion,
  summarizeModelTest,
  testLatencyText,
} from "../lib/modelTest";
import {
  buildModelDetectPayload,
  channelInterfaceUnion,
  detectAppliedLabels,
  detectProtocolChips,
  interfaceChipsOrUnknown,
  summarizeDetectResult,
} from "../lib/interfaces";

export function ProviderDetailPage() {
  const refresh = useRefresh();
  const { providerID = "", channelID = "" } = useParams();
  const effectiveProviderID = providerID || channelID;
  const navigate = useNavigate();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const [modelDraft, setModelDraft] = useState("");
  // The quick connectivity test is per model, and so is its state: a test in
  // flight on one card must not disable the toolbar or another card's button.
  // The result map keeps the last outcome after the page refetches.
  const [testResults, setTestResults] = useState({});
  const [testingModels, setTestingModels] = useState({});
  // Interface detection has the same per-model shape: one result and one busy
  // flag per card, because the button is on the card.
  const [detectResults, setDetectResults] = useState({});
  const [detectingModels, setDetectingModels] = useState({});
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
  const discoveredDisabledModels = modelsUsage.filter((model) => model.source === "discovered" && !model.enabled && model.state !== "removed").map((model) => model.model);
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

  // Enabling a model now also asks the backend to fill in its unset interfaces
  // (`overwrite: false`): a model that was just switched on is usually one the
  // operator has not probed yet. The detection request carries no credential.
  const detectOnEnable = useWriteMutation({
    mutationFn: ({ model }) => postJSON(apiPaths.providerModelDetect, buildModelDetectPayload({ channelID: effectiveProviderID, model, overwrite: false })),
    error: "channels.detectFailed",
    onSuccess: reload,
  });

  const setModelEnabled = useWriteMutation({
    mutationFn: ({ model, enabled }) => patchJSON(apiPaths.providerModel(effectiveProviderID, model), { enabled }),
    error: "channelDetail.updateModelError",
    onSuccess: (_data, variables) => {
      reload();
      if (variables?.enabled && variables.model) {
        detectOnEnable.mutate({ model: variables.model });
      }
    },
  });

  const saveModelProtocols = useWriteMutation({
    mutationFn: ({ model, protocols }) => patchJSON(apiPaths.providerModel(effectiveProviderID, model), { protocols }),
    success: "channelDetail.testSaved",
    error: "channelDetail.updateModelError",
    onSuccess: reload,
  });

  // The explicit per-model detect button overwrites the stored interfaces.
  const detectModel = useWriteMutation({
    mutationFn: ({ model }) => postJSON(apiPaths.providerModelDetect, buildModelDetectPayload({ channelID: effectiveProviderID, model, overwrite: true })),
    success: (result) => t("channelDetail.detectApplied", { count: formatCount(summarizeDetectResult(result).appliedCount) }),
    error: "channels.detectFailed",
    onSuccess: (result, variables) => {
      const model = variables?.model;
      if (!model) {
        return;
      }
      setDetectResults((current) => ({ ...current, [model]: result }));
      setDetectingModels((current) => removingKey(current, model));
      reload();
    },
    onError: (_err, variables) => {
      const model = variables?.model;
      if (model) {
        setDetectingModels((current) => removingKey(current, model));
      }
    },
  });

  // The toolbar action detects every enabled model at once: the body is only the
  // channel id, so the backend picks the models.
  const detectAll = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providerModelDetect, buildModelDetectPayload({ channelID: effectiveProviderID })),
    success: (result) => {
      const summary = summarizeDetectResult(result);
      return `${t("channels.detected")} · ${t("channels.detectApplied", { count: formatCount(summary.appliedCount) })}`;
    },
    error: "channels.detectFailed",
    onSuccess: reload,
  });

  const deleteProvider = useWriteMutation({
    mutationFn: () => deleteJSON(apiPaths.provider(effectiveProviderID)),
    error: "channelDetail.deleteProviderError",
    onSuccess: () => navigate("/channels"),
  });

  const deleteModel = useWriteMutation({
    mutationFn: ({ model }) => deleteJSON(apiPaths.providerModel(effectiveProviderID, model)),
    error: "channelDetail.deleteModelError",
    onSuccess: reload,
  });

  // The quick connectivity test reports inline rather than through the page's
  // shared `busy` string, so its pending state is tracked per model. The request
  // body carries no credential: the channel already stores the API key.
  const testModel = useWriteMutation({
    mutationFn: ({ model }) => postJSON(apiPaths.providerModelTest, buildModelTestPayload({ channelID: effectiveProviderID, model })),
    error: "channelDetail.testError",
    onSuccess: (result, variables) => {
      const model = variables?.model;
      if (!model) {
        return;
      }
      setTestResults((current) => ({ ...current, [model]: result }));
      setTestingModels((current) => removingKey(current, model));
    },
    onError: (_err, variables) => {
      const model = variables?.model;
      if (model) {
        setTestingModels((current) => removingKey(current, model));
      }
    },
  });

  // The one-click fix for "chat works, /v1/responses does not": turn Responses
  // off for this model. The result keeps its observation but loses the action,
  // so a second click cannot be offered against an already-applied suggestion.
  const disableResponses = useWriteMutation({
    mutationFn: ({ model }) => patchJSON(apiPaths.providerModel(effectiveProviderID, model), disableResponsesPayload()),
    success: "channelDetail.testSaved",
    error: "channelDetail.updateModelError",
    onSuccess: (_payload, variables) => {
      const model = variables?.model;
      if (model) {
        setTestResults((current) => (current[model] ? { ...current, [model]: applyResponsesDisabled(current[model]) } : current));
      }
      reload();
    },
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

  const runModelTest = (model) => {
    if (!model || testingModels[model]) {
      return;
    }
    setTestingModels((current) => ({ ...current, [model]: true }));
    testModel.mutate({ model });
  };

  const runDisableResponses = (model) => {
    if (model) {
      disableResponses.mutate({ model });
    }
  };

  const runModelDetect = (model) => {
    if (!model || detectingModels[model]) {
      return;
    }
    setDetectingModels((current) => ({ ...current, [model]: true }));
    detectModel.mutate({ model });
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
              <DetailMetaPill label={t("channelDetail.baseUrl")} value={provider.base_url || "-"} mono />
              <DetailMetaPill label={t("channelDetail.models")} value={`${formatCount(provider.enabled_model_count)} / ${formatCount(provider.model_count)}`} />
              <DetailMetaPill label={t("common.requests")} value={formatCount(summary.request_count)} />
              <DetailMetaPill label={t("common.tokens")} value={formatCount(summary.total_tokens)} />
              {summary.missing_usage_request ? <DetailMetaPill label={t("channelDetail.missingUsage")} value={formatCount(summary.missing_usage_request)} /> : null}
            </div>
            <InterfaceChipRow label={t("channels.interfaceSummary")} chips={channelInterfaceUnion(modelsUsage)} />
          </div>
          <div className="topbar-meta detail-toolbar">
            <div className="detail-toolbar-actions">
              <Button asChild variant="default" size="icon">
                <Link to="/channels" title={t("channelDetail.backToProviders")} aria-label={t("channelDetail.backToProviders")}>
                  <HomeIcon />
                </Link>
              </Button>
              <Button variant="default" size="icon" type="button" onClick={() => detectAll.mutate()} disabled={detectAll.isPending || !provider.enabled_model_count} title={detectAll.isPending ? t("channels.detectingAll") : t("channels.detectAll")} aria-label={t("channels.detectAll")}>
                {detectAll.isPending ? <span className="provider-model-test-spinner" aria-hidden="true" /> : <DetectIcon />}
              </Button>
              <Button variant="default" size="icon" type="button" onClick={() => probe.mutate()} disabled={busy === "probe"} title={t("channelDetail.probeProvider")} aria-label={t("channelDetail.probeProvider")}><ProbeIcon /></Button>
              <DialogTrigger asChild>
                <Button variant="default" size="icon" type="button" title={t("channelDetail.editProvider")} aria-label={t("channelDetail.editProvider")}><EditIcon /></Button>
              </DialogTrigger>
              <Button variant="default" size="icon" type="button" onClick={confirmDeleteProvider} disabled={busy === "delete-provider"} title={t("providers.deleteTitle")} aria-label={t("providers.deleteTitle")}><DeleteIcon /></Button>
              <Switch checked={Boolean(provider.enabled)} onChange={(enabled) => setProviderEnabled.mutate(enabled)} disabled={busy === "provider"} label={t("channelDetail.providerEnabled")} />
            </div>
            <WindowToggle value={windowValue} onChange={setWindow} label={t("channelDetail.windowLabel")} />
          </div>
        </header>

        <Card as="section">
          <div className="panel-head">
            <div>
              <h2>{t("channelDetail.providerUsage")}</h2>
            </div>
          </div>
          <div className="hero-grid hero-grid-compact">
            <StatCard label={t("common.requests")} value={formatCount(summary.request_count)} />
            <StatCard label={t("common.errors")} value={formatCount(summary.failed_request)} accent={summary.failed_request ? "accent-red" : ""} />
            <StatCard label={t("common.tokens")} value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
            <StatCard label={t("common.success")} value={`${Number(summary.success_rate || 0).toFixed(1)}%`} />
            <StatCard label={t("overview.rpm")} value={formatCount(summary.current_rpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_rpm ?? 0) })} />
            <StatCard label={t("overview.tpm")} value={formatCount(summary.current_tpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_tpm ?? 0) })} />
          </div>
        </Card>

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
            onSave={() => saveProvider.mutate()}
          />
        ) : null}

        {detail.data ? (
          <>
            <Card as="section">
              <div className="panel-head">
                <div>
                  <h2>{t("channelDetail.tokenRequestBuckets")}</h2>
                </div>
              </div>
              <SingleUsageCharts items={trends} />
            </Card>

            <Card as="section">
              <div className="panel-head">
                <div>
                  <h2>{t("channelDetail.modelRoutingUsage")}</h2>
                  <p className="trace-subline">{t("channelDetail.enableHint")}</p>
                  <p className="trace-subline">{t("channelDetail.autoDetectOnEnable")}</p>
                  {!provider.enabled ? <p className="trace-subline">{t("channelDetail.providerDisabledHint")}</p> : null}
                </div>
              </div>
              <form className="filter-bar" onSubmit={submitAddModel}>
                <Input className="min-w-[260px]" type="search" value={modelDraft} onChange={(event) => setModelDraft(event.target.value)} placeholder={t("channelDetail.addModelPlaceholder")} />
                <Button variant="primary" type="submit" disabled={busy === "add-model"}>{busy === "add-model" ? t("channelDetail.adding") : t("channelDetail.addModel")}</Button>
                <Button variant="ghost" type="button" onClick={() => toggleModels(discoveredDisabledModels, true)} disabled={!discoveredDisabledModels.length || busy === "models-enable"}>{busy === "models-enable" ? t("channelDetail.enabling") : t("channelDetail.enableDiscovered", { count: formatCount(discoveredDisabledModels.length) })}</Button>
                <Button variant="ghost" type="button" onClick={() => probe.mutate()} disabled={busy === "probe"}>{busy === "probe" ? t("channelDetail.syncing") : t("channelDetail.syncModels")}</Button>
              </form>
              {lastProbe?.status === "success" ? (
                <p className="trace-subline">{t("channelDetail.syncSummary", { added: formatCount(lastProbe.added_count || 0), removed: formatCount(lastProbe.removed_count || 0), total: formatCount(lastProbe.discovered_count || 0) })}</p>
              ) : null}
              <div className="provider-model-card-grid">
                {modelsUsage.length ? modelsUsage.map((model) => (
                  <ProviderModelRow
                    key={model.model}
                    item={model}
                    providerEnabled={Boolean(provider.enabled)}
                    busy={busy === model.model}
                    deleting={busy === `delete:${model.model}`}
                    testing={Boolean(testingModels[model.model])}
                    testResult={testResults[model.model] || null}
                    disablingResponses={Boolean(disableResponses.isPending && disableResponses.variables?.model === model.model)}
                    detecting={Boolean(detectingModels[model.model])}
                    detectResult={detectResults[model.model] || null}
                    onToggle={() => setModelEnabled.mutate({ model: model.model, enabled: !model.enabled })}
                    onTest={() => runModelTest(model.model)}
                    onDetect={() => runModelDetect(model.model)}
                    onDisableResponses={() => runDisableResponses(model.model)}
                    onDelete={() => confirmDeleteModel(model.model)}
                    onSaveProtocols={(protocols) => saveModelProtocols.mutate({ model: model.model, protocols })}
                    savingProtocols={saveModelProtocols.isPending}
                  />
                )) : <EmptyState title={t("channelDetail.noModels")} detail={t("channelDetail.noModelsDetail")} compact />}
              </div>
            </Card>

            <Card as="section">
              <div className="panel-head">
                <div>
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
            </Card>

            <Card as="section">
              <div className="panel-head">
                <div>
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
            </Card>
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
  const isCustom = !form.provider_preset;
  const updateForm = (key, value) => {
    onChange((current) => normalizePresetSelection({ ...current, [key]: value }, presetData, key));
  };
  const toggleInterface = (key, checked) => {
    onChange((current) => {
      const interfaces = { ...(current.interfaces || {}), [key]: checked };
      return { ...current, interfaces, interfaces_dirty: true, ...deriveCustomChannelRouting(interfaces, current) };
    });
  };
  const submit = (event) => {
    event.preventDefault();
    onSave();
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
            <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>x</Button>
          </DialogClose>
        </DialogHeader>
        <div className="provider-form provider-form-modal">
          <label>{t("providers.name")}<Input required value={form.name} onChange={(event) => updateForm("name", event.target.value)} /></label>
          <label>{t("providers.preset")}
            <Select value={form.provider_preset} onValueChange={(next) => updateForm("provider_preset", next)}>
              <SelectTrigger aria-label={t("providers.preset")} className="w-full"><SelectValue placeholder={presetState.options[0] || ""} /></SelectTrigger>
              <SelectContent>
                {presetState.options.map((preset) => <SelectItem key={preset} value={preset}>{preset}</SelectItem>)}
              </SelectContent>
            </Select>
          </label>
          <label className="provider-form-wide">{t("providers.baseURL")}<Input required value={form.base_url} onChange={(event) => updateForm("base_url", event.target.value)} /></label>
          <label className="provider-form-wide">{t("providers.apiKey")}<Input type="password" value={form.api_key} onChange={(event) => updateForm("api_key", event.target.value)} placeholder={provider.api_key_hint ? t("channelDetail.keepApiKey", { hint: provider.api_key_hint }) : t("channelDetail.unchanged")} /></label>
          {isCustom ? (
            <fieldset className="provider-interface-fieldset provider-form-wide">
              <legend>{t("providers.supportedInterfaces")}</legend>
              <div className="provider-interface-grid">
                {INTERFACE_OPTIONS.map(([key, labelKey]) => (
                  <label className="checkbox-row" key={key}>
                    <input type="checkbox" checked={Boolean(form.interfaces?.[key])} onChange={(event) => toggleInterface(key, event.target.checked)} />
                    {t(labelKey)}
                  </label>
                ))}
              </div>
            </fieldset>
          ) : null}
        </div>
        <p className="trace-subline"><strong>{t("channels.autoDetect")}</strong> · {t("channels.autoDetectHint")}</p>
        <Button variant="ghost" type="button" onClick={() => setAdvancedOpen((open) => !open)}>{advancedOpen ? t("providers.hideAdvanced") : t("providers.advanced")}</Button>
        {advancedOpen ? (
          <div className="provider-form provider-form-modal">
            <ProviderAdvancedFields form={form} presetState={presetState} onChange={updateForm} includeHeaders showProtocolSelectors={!isCustom} />
          </div>
        ) : null}
        <DialogFooter>
          <Button variant="ghost" type="button" onClick={onReset}>{t("common.reset")}</Button>
          <Button variant="ghost" type="button" onClick={onClose}>{t("providers.cancel")}</Button>
          <Button variant="primary" type="submit" disabled={saving}>{saving ? t("common.saving") : t("channelDetail.saveChanges")}</Button>
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
    <Card as="section">
      <div className="panel-head">
        <div>
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
        <Button variant="primary" type="button" onClick={onApply} disabled={busy || report.status !== "detected"}>{busy ? t("providers.applying") : t("providers.applySuggestions")}</Button>
      </div>
    </Card>
  );
}

function ProviderModelRow({
  item,
  providerEnabled,
  busy,
  deleting,
  testing,
  testResult,
  disablingResponses,
  detecting,
  detectResult,
  onToggle,
  onTest,
  onDetect,
  onDisableResponses,
  onDelete,
  onSaveProtocols,
  savingProtocols,
}) {
  const { t } = useI18n();
  const summary = item.summary || {};
  const state = item.state || (item.enabled ? "enabled" : "disabled");
  // A trace-discovered model is a history row, not a configured route: there is
  // nothing to test until it has been added, and no card action is offered.
  const testable = item.source !== "trace";
  const canDelete = testable;
  const testLabel = testing ? t("channelDetail.testing") : t("channelDetail.testModel");
  const stateLabel = {
    enabled: t("channelDetail.stateEnabled"),
    disabled: t("channelDetail.stateDisabled"),
    removed: t("channelDetail.stateRemoved"),
    unverified: t("channelDetail.stateUnverified"),
  }[state] || state;
  const stateTone = state === "removed" ? "danger" : state === "unverified" ? "gold" : item.enabled ? "green" : "default";
  return (
    <div className="provider-model-card">
      <div className="provider-model-card-head">
        <div>
          <strong>{item.model}</strong>
          <span>{modelSourceLabel(item.source)}</span>
        </div>
        <div className="action-group">
          {item.source !== "trace" ? <Switch checked={Boolean(item.enabled)} onChange={onToggle} disabled={busy} label={t("channelDetail.modelEnabled", { model: item.model })} /> : <span>{t("channelDetail.historyOnly")}</span>}
          {testable ? (
            <Button variant="default" size="icon" type="button" onClick={onDetect} disabled={detecting} title={detecting ? t("channelDetail.detecting") : t("channelDetail.detectInterfaces")} aria-label={t("channelDetail.detectInterfaceAria", { model: item.model })}>
              {detecting ? <span className="provider-model-test-spinner" aria-hidden="true" /> : <DetectIcon />}
            </Button>
          ) : null}
          {testable ? (
            <Button variant="default" size="icon" type="button" onClick={onTest} disabled={testing} title={testLabel} aria-label={t("channelDetail.testModelAria", { model: item.model })}>
              {testing ? <span className="provider-model-test-spinner" aria-hidden="true" /> : <TestIcon />}
            </Button>
          ) : null}
          {canDelete ? (
            <Button variant="default" size="icon" type="button" onClick={onDelete} disabled={deleting} title={t("channelDetail.deleteModel")} aria-label={t("channelDetail.deleteModelAria", { model: item.model })}>
              <DeleteIcon />
            </Button>
          ) : null}
        </div>
      </div>
      <div className="trace-tag-group">
        <InlineTag tone={item.source === "trace" ? "default" : stateTone}>{item.source === "trace" ? t("channelDetail.historyOnlyTag") : stateLabel}</InlineTag>
        {item.upstream_model ? <InlineTag title={t("channelDetail.upstreamNameHint")}>{`→ ${item.upstream_model}`}</InlineTag> : null}
        {state === "removed" ? <InlineTag tone="danger">{t("channelDetail.removedHint")}</InlineTag> : null}
        {state === "unverified" ? <InlineTag tone="gold">{t("channelDetail.upstreamNameHint")}</InlineTag> : null}
        {item.enabled && !providerEnabled ? <InlineTag tone="gold">{t("channelDetail.blockedProviderDisabled")}</InlineTag> : null}
      </div>
      <InterfaceChipRow chips={interfaceChipsOrUnknown(item.interfaces)} />
      <ModelProtocolEditor item={item} onSave={onSaveProtocols} saving={savingProtocols} />
      {detecting ? <ModelTestPending labelKey="channelDetail.detecting" /> : detectResult ? <ModelDetectResult result={detectResult} /> : null}
      {testing ? <ModelTestPending /> : testResult ? (
        <ModelTestResult result={testResult} disabling={disablingResponses} onDisableResponses={onDisableResponses} />
      ) : null}
      <div className="model-market-metrics model-market-metrics-compact">
        <Metric label="req" value={formatCount(summary.request_count)} />
        <Metric label="err" value={formatCount(summary.failed_request)} danger={Number(summary.failed_request || 0) > 0} />
        <Metric label="tok" value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
        <Metric label="rpm" value={formatCount(summary.current_rpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_rpm ?? 0) })} />
        <Metric label="tpm" value={formatCount(summary.current_tpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_tpm ?? 0) })} />
      </div>
    </div>
  );
}

const MODEL_PROTOCOLS = [
  ["chat_completions", "channels.interfaceChat"],
  ["responses", "channels.interfaceNativeResponses"],
  ["messages", "channels.interfaceMessages"],
  ["gemini", "channels.interfaceGenerate"],
];

function ModelProtocolEditor({ item, onSave, saving }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(item.protocols || {});
  useEffect(() => setDraft(item.protocols || {}), [item.protocols]);
  const update = (protocol, key, value) => {
    setDraft((current) => {
      const existing = current[protocol] || { enabled: false, wire_model: "" };
      const next = { ...existing, [key]: value };
      // Typing a wire model for an inherited protocol is an explicit opt-in;
      // otherwise the row would save as disabled and silently drop the
      // protocol the operator just configured.
      if (!current[protocol] && key === "wire_model" && String(value).trim() !== "") {
        next.enabled = true;
      }
      return { ...current, [protocol]: next };
    });
  };
  return (
    <div className="provider-model-protocols">
      <Button variant="ghost" size="sm" type="button" onClick={() => setOpen((value) => !value)}>
        {open ? t("common.hide") : t("channelDetail.protocols")}
      </Button>
      {open ? (
        <div className="provider-form">
          <p className="trace-subline provider-form-wide">{t("channelDetail.protocolsHint")}</p>
          {MODEL_PROTOCOLS.map(([protocol, labelKey]) => {
            const entry = draft[protocol] || { enabled: false, wire_model: "" };
            return (
              <div className="provider-form-wide" key={protocol} style={{ display: "grid", gridTemplateColumns: "1fr 1.4fr auto", gap: 8, alignItems: "center" }}>
                <span>{t(labelKey)}</span>
                <Input value={entry.wire_model || ""} onChange={(event) => update(protocol, "wire_model", event.target.value)} placeholder={t("channelDetail.protocolWireModel")} />
                <Switch checked={Boolean(entry.enabled)} onChange={(enabled) => update(protocol, "enabled", enabled)} label={`${t(labelKey)} enabled`} />
              </div>
            );
          })}
          <div className="provider-form-actions provider-form-wide">
            <Button variant="primary" type="button" disabled={saving} onClick={() => onSave(draft)}>{saving ? t("common.saving") : t("common.save")}</Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ModelTestPending({ labelKey = "channelDetail.testing" }) {
  const { t } = useI18n();
  return (
    <div className="provider-model-test-result provider-model-test-result-pending" role="status" aria-live="polite">
      <span className="provider-model-test-spinner" aria-hidden="true" />
      <span>{t(labelKey)}</span>
    </div>
  );
}

// Markers are characters, not colours: the chip's tone only reinforces what
// the glyph and the words already say. That is also why the failing chips print
// the HTTP status next to the status label.
const TEST_MARKERS = {
  check: "✓",
  cross: "✗",
  skip: "–",
};

function ModelTestResult({ result, disabling, onDisableResponses }) {
  const { t } = useI18n();
  const summary = summarizeModelTest(result);
  const suggestion = disableResponsesSuggestion(result);
  const warnings = (Array.isArray(result.warnings) ? result.warnings : []).filter((warning) => typeof warning === "string" && warning.trim());
  const summaryLabel = summary.summaryLabelKey ? t(summary.summaryLabelKey) : summary.status;
  return (
    <div className="provider-model-test-result" role="status" aria-live="polite">
      <div className="provider-model-test-head">
        <span className="provider-model-test-title">{t("channelDetail.testResultTitle")}</span>
        {summaryLabel ? <InlineTag tone={summary.tone}>{summaryLabel}</InlineTag> : null}
        {testLatencyText(t, summary.durationMs) ? <span className="provider-model-test-duration">{testLatencyText(t, summary.durationMs)}</span> : null}
      </div>
      {summary.chips.length ? (
        <div className="provider-model-test-chips">
          {summary.chips.map((chip) => {
            const parts = [chip.label || chip.protocol];
            const latencyText = testLatencyText(t, chip.latencyMs);
            if (latencyText) {
              parts.push(latencyText);
            }
            // The success chip is the bare protocol and its latency, as in
            // "✓ Chat Completions · 314 ms": repeating "ok" adds no information.
            // A failing or skipped one names the outcome after the status code.
            if (chip.marker !== "check") {
              const statusText = chip.labelKey ? t(chip.labelKey) : chip.status;
              parts.push(chip.httpStatus ? `${chip.httpStatus} ${statusText}` : statusText);
            }
            return (
              <span
                key={chip.protocol}
                className={`provider-model-test-chip provider-model-test-chip-${chip.tone}`}
                title={chip.title || undefined}
              >
                {/* The marker is decorative here because the text beside it
                    already states the outcome; the trailing space keeps the
                    chip's text content readable as one string. */}
                <span className="provider-model-test-mark" aria-hidden="true">{`${TEST_MARKERS[chip.marker] || "–"} `}</span>
                <span>{parts.join(" · ")}</span>
              </span>
            );
          })}
        </div>
      ) : null}
      {suggestion && !result.responses_disabled ? (
        <div className="provider-model-test-suggestion">
          <p className="provider-model-test-suggest">{t("channelDetail.testResponsesSuggest")}</p>
          {suggestion.message ? <p className="provider-model-test-suggest-detail">{suggestion.message}</p> : null}
          <div className="provider-model-test-actions">
            <Button variant="primary" size="sm" type="button" onClick={onDisableResponses} disabled={disabling}>
              {disabling ? t("common.saving") : t("channelDetail.testDisableResponses")}
            </Button>
          </div>
        </div>
      ) : null}
      {result.responses_disabled ? (
        <div className="provider-model-test-applied">
          <InlineTag tone="green">{t("channelDetail.testResponsesDisabled")}</InlineTag>
        </div>
      ) : null}
      {warnings.length ? (
        <ul className="provider-model-test-warnings">
          {warnings.map((warning) => <li key={warning}>{warning}</li>)}
        </ul>
      ) : null}
    </div>
  );
}

// The chip row is shared by the channel header (union over enabled models) and
// each model card (that model's interfaces). The unknown chip keeps an
// undetected model from looking like a model with no interfaces at all.
function InterfaceChipRow({ chips, label = "" }) {
  const { t } = useI18n();
  if (!chips || !chips.length) {
    return null;
  }
  return (
    <div className="channel-interface-chips" role="list" aria-label={label || undefined}>
      {chips.map((chip) => (
        <span key={chip.key} role="listitem">
          <InlineTag tone={chip.tone}>{t(chip.labelKey)}</InlineTag>
        </span>
      ))}
    </div>
  );
}

// The inline result of one detect action. It says what was written, which
// protocols were probed and how they answered, whether the action overwrote the
// previous flags, and the skip reason when the detector found nothing.
function ModelDetectResult({ result }) {
  const { t } = useI18n();
  const summary = summarizeDetectResult(result);
  const item = summary.items[0] || {};
  const appliedLabels = detectAppliedLabels(item);
  const protocols = detectProtocolChips(item);
  const skipped = typeof item.skipped_reason === "string" ? item.skipped_reason.trim() : "";
  const duration = testLatencyText(t, summary.durationMs);
  return (
    <div className="provider-model-detect-result" role="status" aria-live="polite">
      <div className="provider-model-test-head">
        <span className="provider-model-test-title">{t("channels.detectResultTitle")}</span>
        <InlineTag tone={appliedLabels.length ? "green" : skipped ? "gold" : "default"}>
          {appliedLabels.length
            ? t("channels.detectApplied", { count: formatCount(appliedLabels.length) })
            : skipped
              ? t("channels.interfaceUnknown")
              : t("channels.detectFailed")}
        </InlineTag>
        <span className="channel-detect-mode">{summary.overwrite ? t("channels.detectOverwrite") : t("channels.detectFillOnly")}</span>
        {duration ? <span className="provider-model-test-duration">{duration}</span> : null}
      </div>
      {appliedLabels.length ? (
        <div className="channel-interface-chips">
          {appliedLabels.map((labelKey) => <InlineTag key={labelKey} tone="green">{t(labelKey)}</InlineTag>)}
        </div>
      ) : null}
      {protocols.length ? (
        <div className="provider-model-test-chips">
          {protocols.map((chip) => {
            const parts = [chip.label || chip.protocol];
            const latencyText = testLatencyText(t, chip.latencyMs);
            if (latencyText) {
              parts.push(latencyText);
            }
            if (chip.marker !== "check") {
              const statusText = chip.labelKey ? t(chip.labelKey) : chip.status;
              parts.push(chip.httpStatus ? `${chip.httpStatus} ${statusText}` : statusText);
            }
            return (
              <span key={chip.protocol} className={`provider-model-test-chip provider-model-test-chip-${chip.tone}`}>
                <span className="provider-model-test-mark" aria-hidden="true">{`${TEST_MARKERS[chip.marker] || "–"} `}</span>
                <span>{parts.join(" · ")}</span>
              </span>
            );
          })}
        </div>
      ) : null}
      {skipped ? <p className="channel-detect-skipped">{t("channels.detectSkipped", { reason: skipped })}</p> : null}
      {summary.warnings.length ? (
        <ul className="provider-model-test-warnings">
          {summary.warnings.map((warning) => <li key={warning}>{warning}</li>)}
        </ul>
      ) : null}
    </div>
  );
}

function removingKey(record, key) {
  const next = { ...record };
  delete next[key];
  return next;
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
    headers_text: "",
    proxy_enabled: false,
    proxy_http: "",
    proxy_https: "",
    proxy_no_proxy: "",
    endpoints: {},
    // The four checked wire protocols of a custom channel; official channels
    // keep the protocol selectors instead.
    interfaces: { chat_completions: false, responses: false, messages: false, gemini: false },
    // Unedited capability keys (tool_calling, embeddings, ...) ride along so
    // an edit never narrows the stored declaration.
    capability_seed: {},
  };
}

function editFormFromProvider(provider = {}) {
  const headers = provider.headers || {};
  return {
    name: provider.name || "",
    base_url: provider.base_url || "",
    provider_preset: provider.provider_preset || "",
    api_type: provider.api_type || "chat_completions",
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
    headers_text: Object.keys(headers).sort().map((key) => `${key}: ${headers[key]}`).join("\n"),
    proxy_enabled: Boolean(provider.proxy?.enabled),
    proxy_http: provider.proxy?.http_proxy || "",
    proxy_https: provider.proxy?.https_proxy || "",
    proxy_no_proxy: provider.proxy?.no_proxy || "",
    endpoints: provider.capabilities?.endpoints || provider.endpoints || {},
    interfaces: interfacesFromCapabilities(provider.capabilities),
    capability_seed: provider.capabilities && typeof provider.capabilities === "object" ? { ...provider.capabilities } : {},
  };
}

function providerPayloadFromForm(form) {
  const payload = {
    name: form.name,
    base_url: form.base_url,
    provider_preset: form.provider_preset,
    api_type: form.api_type,
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
    endpoints: form.endpoints || {},
    headers: parseHeadersText(form.headers_text),
    proxy: {
      enabled: Boolean(form.proxy_enabled),
      http_proxy: String(form.proxy_http || "").trim(),
      https_proxy: String(form.proxy_https || "").trim(),
      no_proxy: String(form.proxy_no_proxy || "").trim(),
    },
  };
  if (form.api_key.trim()) {
    payload.api_key = form.api_key.trim();
  }
  if (!form.provider_preset) {
    // A custom channel is analysed by the protocols the operator checks; the
    // internal family/profile projection is derived, never asked for. The
    // unedited capability keys ride along from the seed so an edit never
    // narrows a stored declaration. capabilities are only rewritten when a box
    // was actually ticked, so a name-only edit of a legacy channel that never
    // declared capabilities does not flip unset keys to false.
    const interfaces = form.interfaces || {};
    Object.assign(payload, deriveCustomChannelRouting(interfaces, payload));
    if (form.interfaces_dirty) {
      payload.capabilities = { ...(form.capability_seed || {}), ...capabilitiesFromInterfaces(interfaces) };
    }
  }
  return payload;
}

function providerProbeSuggestionPayload(_provider = {}, report = {}) {
  // Only the channel-level API surface is a suggestion now: interface support is
  // detected per model and written to the model rows.
  const payload = {};
  if (report.suggested_api_type) {
    payload.api_type = report.suggested_api_type;
  }
  if (report.suggested_protocol_family) {
    payload.protocol_family = report.suggested_protocol_family;
  }
  return payload;
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
