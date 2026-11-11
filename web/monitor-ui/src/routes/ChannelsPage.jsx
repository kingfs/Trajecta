import React, { useMemo, useState } from "react";
import { toast } from "sonner";
import { Card } from "../components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Input } from "../components/ui/input";
import { Button } from "../components/ui/button";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { PageHeader } from "../components/common/PageHeader";
import { WindowToggle } from "../components/common/Tabs";
import { DeleteIcon, InlineTag, PlusIcon } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { MultiLineChart } from "../components/common/Charts";
import { ChannelRoutingPanels } from "../components/monitor/ChannelRoutingPanels";
import { Switch } from "../components/common/Controls";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../components/ui/dialog";
import { useJSON } from "../hooks/useJSON";
import { useRefresh } from "../hooks/useRefresh";
import { apiPaths, apiURL, deleteJSON, patchJSON, postJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { buildProviderLink, formatCount, formatDateTime, formatRateAvg, normalizeAnalyticsWindow, setOrDeleteParam } from "../lib/monitor";

// The create form asks only for what a channel actually needs. Interface
// capabilities are detected per model now, so the legacy channel-level
// capability, mode and discovery fields are not part of the form or the write.
const DEFAULT_FORM = {
  name: "",
  base_url: "",
  provider_preset: "openai",
  api_type: "chat_completions",
  protocol_family: "openai_compatible",
  routing_profile: "openai_default",
  api_version: "",
  deployment: "",
  project: "",
  location: "",
  model_resource: "",
  api_key: "",
  enabled: true,
  priority: 100,
  weight: 1,
  capacity_hint: 1,
  proxy_enabled: false,
  proxy_http: "",
  proxy_https: "",
  proxy_no_proxy: "",
  endpoints: {},
};

export function ProvidersPage() {
  const refresh = useRefresh();
  const navigate = useNavigate();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const [formOpen, setFormOpen] = useState(false);
  const params = new URLSearchParams();
  params.set("window", windowValue);
  const providers = useJSON(apiURL(apiPaths.providers, params), [windowValue]);
  const presets = useJSON(apiPaths.providerPresets, []);
  const catalog = useJSON(apiPaths.providerCatalog, []);
  const items = providers.data?.items || [];
  const totals = useMemo(() => summarizeProviders(items), [items]);
  const chartItems = useMemo(() => buildProviderTrendItems(items), [items]);
  const chartSeries = useMemo(() => items.map((item) => ({ key: item.id, name: item.name || item.id })), [items]);

  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    setSearchParams(next);
  };

  return (
    <div className="shell shell-list">
      <PageHeader
        title={t("providers.title")}
        actions={<WindowToggle value={windowValue} onChange={setWindow} label={t("providers.analyticsWindow")} />}
      />

      <section className="hero-grid" aria-label={t("providers.managed")}>
        <StatCard label={t("providers.title")} value={formatCount(items.length)} />
        <StatCard label={t("providers.enabled")} value={formatCount(totals.enabled)} />
        <StatCard label={t("overview.requests")} value={formatCount(totals.requests)} />
        <StatCard label={t("overview.tokens")} value={formatCount(totals.tokens)} detail={usageCoverageDetail(totals.missing, t)} />
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("providers.managed")}</h2>
          </div>
        </div>
        <div className="usage-chart-grid chart-grid-two">
          <section className="usage-chart-panel">
            <div className="breakdown-title">{t("providers.requestsByProvider")}</div>
            <MultiLineChart items={chartItems} series={chartSeries} metric="request_count" />
          </section>
          <section className="usage-chart-panel">
            <div className="breakdown-title">{t("providers.tokensByProvider")}</div>
            <MultiLineChart items={chartItems} series={chartSeries} metric="total_tokens" />
          </section>
        </div>
      </Card>

      {providers.error ? <EmptyState title={t("providers.loadError")} detail={providers.error} tone="danger" /> : null}
      {providers.loading && !providers.data ? <EmptyState title={t("providers.loading")} detail={t("providers.loadingDetail")} /> : null}
      {providers.data ? (
        <Card as="section">
          <div className="panel-head">
            <div>
              <h2>{t("providers.cards")}</h2>
            </div>
            <div className="panel-head-actions">
              <Button variant="primary" className="icon-text-button" type="button" onClick={() => setFormOpen(true)}>
                <PlusIcon />
                <span>{t("providers.new")}</span>
              </Button>
            </div>
          </div>
          <div className="provider-grid">
            {items.length ? items.map((item) => <ProviderCard key={item.id} item={item} windowValue={windowValue} onRefresh={refresh} />) : <EmptyState title={t("providers.none")} detail={t("providers.noneDetail")} />}
          </div>
        </Card>
      ) : null}
      {formOpen ? (
        <CreateProviderDialog
          presetData={presets.data}
          catalog={catalog}
          onClose={() => setFormOpen(false)}
          onCreated={(item) => {
            setFormOpen(false);
            refresh();
            if (item?.id) navigate(buildProviderLink(item.id, windowValue));
          }}
        />
      ) : null}
      {/* Routing settings and the routing inspector live on the channel page
          now; the standalone /routing route only redirects here. */}
      <ChannelRoutingPanels />
    </div>
  );
}

function ProviderProbeBatchRow({ row }) {
  const { t } = useI18n();
  return (
    <div className={row.status === "error" ? "provider-probe-card provider-probe-card-failed" : "provider-probe-card"}>
      <div className="provider-probe-card-head">
        <div>
          <p className="eyebrow">{row.providerID || t("providers.providerFallback")}</p>
          <h3>{row.providerName || row.providerID || row.baseURL || t("providers.unnamed")}</h3>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone={row.status === "detected" ? "green" : row.status === "error" ? "danger" : "gold"}>{row.status || "unknown"}</InlineTag>
          {row.fillableCount ? <InlineTag tone="accent">{t("providers.fillable", { count: row.fillableCount })}</InlineTag> : null}
        </div>
      </div>
      <div className="detail-meta-strip">
        <Metric label={t("providers.apiType")} value={row.suggestedAPIType || "-"} detail={row.apiTypeFillable ? t("providers.missing") : row.currentAPIType ? t("providers.set") : ""} />
        <Metric label={t("providers.protocol")} value={row.suggestedProtocolFamily || "-"} detail={row.protocolFillable ? t("providers.missing") : row.currentProtocolFamily ? t("providers.set") : ""} />
        <Metric label={t("providers.capabilities")} value={row.capabilities.length ? row.capabilities.join(", ") : "-"} detail={row.fillableCapabilities.length ? t("providers.unset", { count: row.fillableCapabilities.length }) : ""} />
      </div>
      {row.warnings.length ? <p className="trace-subline">{row.warnings.join(" · ")}</p> : null}
      {!row.fillableCount && row.status === "detected" ? <p className="trace-subline">{t("providers.suggestionsExplicit")}</p> : null}
    </div>
  );
}

const FAMILY_INTERFACES = {
  openai_compatible: { chat_completions: true, responses: false, messages: false, gemini: false },
  anthropic_messages: { chat_completions: false, responses: false, messages: true, gemini: false },
  google_genai: { chat_completions: false, responses: false, messages: false, gemini: true },
  vertex_native: { chat_completions: false, responses: false, messages: false, gemini: true },
};

const FAMILY_ROUTING = {
  openai_compatible: "openai_default",
  anthropic_messages: "anthropic_default",
  google_genai: "google_ai_studio",
  vertex_native: "vertex_express",
};

export const INTERFACE_OPTIONS = [
  ["chat_completions", "providers.interfaceChat"],
  ["responses", "providers.interfaceResponses"],
  ["messages", "providers.interfaceMessages"],
  ["gemini", "providers.interfaceGemini"],
];

function protocolLabels(protocols, t) {
  const labels = {
    chat_completions: t("providers.interfaceChat"),
    responses: t("providers.interfaceResponses"),
    messages: t("providers.interfaceMessages"),
    gemini: t("providers.interfaceGemini"),
  };
  return (protocols || []).map((protocol) => labels[protocol] || protocol);
}

function familyInterfaces(family) {
  return { ...(FAMILY_INTERFACES[family] || FAMILY_INTERFACES.openai_compatible) };
}

function primaryAPIType(interfaces) {
  if (interfaces?.chat_completions) return "chat_completions";
  if (interfaces?.responses) return "responses";
  if (interfaces?.messages) return "messages";
  if (interfaces?.gemini) return "gemini_generate_content";
  return "";
}

// The custom-channel protocol vocabulary is the four wire protocols the
// operator checks. The stored protocol_family/routing_profile are an internal
// projection of that selection, not a separate user-facing choice: Chat
// Completions and Responses are the OpenAI-compatible surface, Messages is
// anthropic_messages, and generateContent is google_genai. Cross-family
// leftovers (a chat-capable channel that also serves Messages) keep working
// through the capability declaration instead of a family.
export function familyForInterfaces(interfaces, fallback = "openai_compatible") {
  if (!interfaces) return fallback;
  if (interfaces.chat_completions || interfaces.responses) return "openai_compatible";
  if (interfaces.messages) return "anthropic_messages";
  if (interfaces.gemini) return "google_genai";
  return fallback;
}

// deriveCustomChannelRouting projects a checkbox selection onto the stored
// api_type/protocol_family/routing_profile. The routing profile is only
// re-derived when the family changes, so an explicitly chosen profile (e.g.
// azure) survives edits that keep the family.
export function deriveCustomChannelRouting(interfaces, current = {}) {
  const currentFamily = current.protocol_family || "openai_compatible";
  const protocolFamily = familyForInterfaces(interfaces, currentFamily);
  return {
    api_type: primaryAPIType(interfaces) || current.api_type || "chat_completions",
    protocol_family: protocolFamily,
    routing_profile:
      protocolFamily !== currentFamily
        ? FAMILY_ROUTING[protocolFamily] || current.routing_profile || ""
        : current.routing_profile || FAMILY_ROUTING[protocolFamily] || "",
  };
}

export function interfacesFromCapabilities(capabilities) {
  const out = { chat_completions: false, responses: false, messages: false, gemini: false };
  if (!capabilities || typeof capabilities !== "object") return out;
  out.chat_completions = capabilities.chat_completions === true;
  out.responses = capabilities.responses === true;
  out.messages = capabilities.messages === true;
  out.gemini = capabilities.gemini_generate_content === true;
  return out;
}

function interfacesFromProtocols(protocols) {
  const out = { chat_completions: false, responses: false, messages: false, gemini: false };
  for (const protocol of protocols || []) {
    if (protocol in out) out[protocol] = true;
  }
  return out;
}

export function capabilitiesFromInterfaces(interfaces = {}) {
  return {
    chat_completions: Boolean(interfaces.chat_completions),
    responses: Boolean(interfaces.responses),
    messages: Boolean(interfaces.messages),
    gemini_generate_content: Boolean(interfaces.gemini),
  };
}

function splitModelList(value) {
  const seen = new Set();
  const out = [];
  for (const raw of String(value || "").split(/[\n,]/)) {
    const model = raw.trim();
    if (!model || seen.has(model.toLowerCase())) continue;
    seen.add(model.toLowerCase());
    out.push(model);
  }
  return out;
}

function CreateProviderDialog({ presetData, catalog, onClose, onCreated }) {
  const { t } = useI18n();
  const [mode, setMode] = useState("official");
  const [form, setForm] = useState(() => ({ ...DEFAULT_FORM, interfaces: familyInterfaces("openai_compatible") }));
  const [selectedCatalogID, setSelectedCatalogID] = useState("");
  const [catalogQuery, setCatalogQuery] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [probeReport, setProbeReport] = useState(null);

  const presetsByID = useMemo(() => {
    const map = new Map();
    for (const preset of presetData?.presets || []) {
      map.set(preset.id, preset);
    }
    return map;
  }, [presetData]);
  const catalogItems = useMemo(() => (Array.isArray(catalog?.data?.items) ? catalog.data.items : []), [catalog]);
  const filteredCatalog = useMemo(() => {
    const query = catalogQuery.trim().toLowerCase();
    if (!query) return catalogItems;
    return catalogItems.filter((item) => {
      const text = [item.id, item.name, ...(item.protocols || [])].join(" ").toLowerCase();
      return text.includes(query);
    });
  }, [catalogItems, catalogQuery]);
  const selectedCatalogItem = catalogItems.find((item) => item.id === selectedCatalogID) || null;
  const selectedPreset = presetsByID.get(selectedCatalogID) || null;
  const presetState = buildPresetState(presetData, form.provider_preset, form.routing_profile);
  const customRoutingState = {
    ...presetState,
    routingOptions: [FAMILY_ROUTING[form.protocol_family] || "", ...presetState.routingOptions].filter((value, index, all) => value && all.indexOf(value) === index),
  };

  const updateForm = (key, value) => {
    setForm((current) => normalizePresetSelection({ ...current, [key]: value }, presetData, key));
  };

  const toggleInterface = (key, checked) => {
    setForm((current) => {
      const interfaces = { ...(current.interfaces || {}), [key]: checked };
      return { ...current, interfaces, ...deriveCustomChannelRouting(interfaces, current) };
    });
  };

  const changeMode = (next) => {
    setMode(next);
    setProbeReport(null);
    if (next === "custom") {
      setSelectedCatalogID("");
      setForm((current) => ({
        ...current,
        provider_preset: "",
        base_url: "",
        endpoints: {},
        api_type: "chat_completions",
        protocol_family: "openai_compatible",
        routing_profile: FAMILY_ROUTING.openai_compatible,
        interfaces: familyInterfaces("openai_compatible"),
      }));
      return;
    }
    setForm((current) => ({ ...current, provider_preset: "", base_url: "", endpoints: {} }));
  };

  const applyCatalogPreset = (item) => {
    const spec = presetsByID.get(item.id) || {};
    const protocols = item.protocols || spec.protocols || [];
    const endpoints = item.endpoints || {};
    const primaryProtocol = protocols.includes("chat_completions")
      ? "chat_completions"
      : protocols.includes("responses")
        ? "responses"
        : protocols.includes("messages")
          ? "messages"
          : protocols.includes("gemini")
            ? "gemini"
            : "chat_completions";
    const family = spec.protocol_family
      || (primaryProtocol === "messages" ? "anthropic_messages" : primaryProtocol === "gemini" ? "google_genai" : "openai_compatible");
    const apiType = primaryProtocol === "messages"
      ? "messages"
      : primaryProtocol === "gemini"
        ? "gemini_generate_content"
        : primaryProtocol === "responses"
          ? "responses"
          : "chat_completions";
    const baseURL = endpoints[primaryProtocol] || endpoints.chat_completions || endpoints.responses || endpoints.messages || endpoints.gemini || "";
    const interfaces = interfacesFromProtocols(protocols);
    setSelectedCatalogID(item.id);
    setProbeReport(null);
    setForm((current) => ({
      ...current,
      name: item.name || item.id,
      provider_preset: item.id,
      api_type: apiType,
      protocol_family: family,
      routing_profile: spec.routing_profile || (Array.isArray(spec.allowed_profiles) ? spec.allowed_profiles[0] : "") || FAMILY_ROUTING[family] || current.routing_profile,
      base_url: baseURL,
      endpoints: { ...endpoints },
      model_resource: family === "vertex_native" && !current.model_resource ? "publishers/google/models" : current.model_resource,
      interfaces,
    }));
  };

  const detectProvider = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providerProbePreview, providerProbePreviewPayload(form)),
    error: "providers.probeFailed",
    onSuccess: (report) => {
      setProbeReport(report);
      setAdvancedOpen(true);
    },
    onError: (err) => {
      if (err.payload?.status) {
        setProbeReport(err.payload);
      }
    },
  });
  const applyProbeSuggestions = () => {
    const report = probeReport || {};
    setForm((current) => {
      const capabilities = Array.isArray(report.capabilities) ? report.capabilities : [];
      const interfaces = capabilities.length
        ? {
            chat_completions: capabilities.includes("chat_completions"),
            responses: capabilities.includes("responses"),
            messages: capabilities.includes("messages"),
            gemini: capabilities.includes("gemini") || capabilities.includes("generate_content"),
          }
        : current.interfaces;
      return {
        ...current,
        api_type: report.suggested_api_type || current.api_type,
        protocol_family: report.suggested_protocol_family || current.protocol_family,
        interfaces,
      };
    });
  };

  const createProvider = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providers, createChannelPayload(form, mode, selectedCatalogID)),
    error: "providers.createFailed",
    onSuccess: (result) => onCreated(result),
  });

  const anyInterface = Object.values(form.interfaces || {}).some(Boolean);
  const canCreate = mode === "official"
    ? Boolean(selectedCatalogID && form.name.trim() && form.base_url.trim())
    : Boolean(form.name.trim() && form.base_url.trim() && anyInterface);

  const submit = (event) => {
    event.preventDefault();
    if (!canCreate) {
      toast.warning(t("providers.createMissingFields"));
      return;
    }
    createProvider.mutate();
  };

  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="provider-create-modal">
        <form onSubmit={submit}>
          <DialogHeader>
            <div>
              <p className="eyebrow">{t("providers.configuration")}</p>
              <DialogTitle>{t("providers.create")}</DialogTitle>
            </div>
            <DialogClose asChild>
              <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>x</Button>
            </DialogClose>
          </DialogHeader>
          <div className="provider-mode-tabs" role="tablist" aria-label={t("providers.create")}>
            <button type="button" role="tab" aria-selected={mode === "official"} className={mode === "official" ? "provider-mode-tab is-active" : "provider-mode-tab"} onClick={() => changeMode("official")}>{t("providers.modeOfficial")}</button>
            <button type="button" role="tab" aria-selected={mode === "custom"} className={mode === "custom" ? "provider-mode-tab is-active" : "provider-mode-tab"} onClick={() => changeMode("custom")}>{t("providers.modeCustom")}</button>
          </div>
          {mode === "official" ? (
            <div className="provider-form provider-form-modal">
              <label>{t("providers.name")}<Input required value={form.name} onChange={(event) => updateForm("name", event.target.value)} placeholder="OpenAI Primary" /></label>
              <label>{t("providers.apiKey")}<Input type="password" value={form.api_key} onChange={(event) => updateForm("api_key", event.target.value)} placeholder="sk-..." /></label>
              <div className="provider-form-wide provider-catalog-panel">
                <div className="provider-catalog-head">
                  <div>
                    <strong>{t("providers.selectProvider")}</strong>
                    <p className="trace-subline">{t("providers.modeOfficialHint")}</p>
                  </div>
                  <Input type="search" value={catalogQuery} onChange={(event) => setCatalogQuery(event.target.value)} placeholder={t("providers.searchProviders")} aria-label={t("providers.searchProviders")} />
                </div>
                <div className="provider-catalog-scroll">
                  <div className="provider-catalog-grid">
                  {filteredCatalog.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      aria-pressed={selectedCatalogID === item.id}
                      className={selectedCatalogID === item.id ? "provider-catalog-card is-selected" : "provider-catalog-card"}
                      onClick={() => applyCatalogPreset(item)}
                    >
                      <strong>{item.name || item.id}</strong>
                      <span>{protocolLabels(item.protocols, t).join(" · ") || "—"}</span>
                    </button>
                  ))}
                  {!filteredCatalog.length ? <p className="trace-subline provider-form-wide">{t("providers.catalogEmpty")}</p> : null}
                  </div>
                </div>
              </div>
              {selectedCatalogItem ? (
                <div className="provider-form-wide provider-selected-summary">
                  <div>
                    <p className="eyebrow">{t("providers.selectedProvider")}</p>
                    <strong>{selectedCatalogItem.name || selectedCatalogItem.id}</strong>
                    <p className="trace-subline">{selectedPreset?.notes || selectedCatalogItem.notes || ""}</p>
                    {selectedCatalogItem.models?.no_list ? <p className="trace-subline">{t("providers.noModelListHint")}</p> : null}
                  </div>
                  <div className="provider-endpoint-summary">
                    <span>{t("providers.baseURL")}</span>
                    <code>{form.base_url || t("providers.baseURLHint")}</code>
                    <span className="trace-subline">{t("providers.officialEndpointHint")}</span>
                  </div>
                </div>
              ) : <p className="trace-subline provider-form-wide">{t("providers.selectProviderHint")}</p>}
              <label className="provider-form-wide">{t("providers.models")}
                <textarea value={form.models} onChange={(event) => updateForm("models", event.target.value)} placeholder="gpt-5&#10;gpt-5-mini" spellCheck={false} />
                <span className="trace-subline">{t("providers.modelsHint")}</span>
              </label>
              <label className="provider-form-check provider-form-wide">{t("providers.enabled")}<Switch checked={Boolean(form.enabled)} onChange={(enabled) => updateForm("enabled", enabled)} label={t("providers.enabled")} /></label>
            </div>
          ) : (
            <div className="provider-form provider-form-modal">
              <label>{t("providers.name")}<Input required value={form.name} onChange={(event) => updateForm("name", event.target.value)} placeholder="My provider" /></label>
              <label>{t("providers.baseURL")}<Input required value={form.base_url} onChange={(event) => updateForm("base_url", event.target.value)} placeholder="https://api.example.com/v1" spellCheck={false} /></label>
              <label>{t("providers.apiKey")}<Input type="password" value={form.api_key} onChange={(event) => updateForm("api_key", event.target.value)} placeholder={t("providers.optional")} /></label>
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
              <label className="provider-form-wide">{t("providers.models")}
                <textarea value={form.models} onChange={(event) => updateForm("models", event.target.value)} placeholder="model-a&#10;model-b" spellCheck={false} />
                <span className="trace-subline">{t("providers.modelsHint")}</span>
              </label>
              <label className="provider-form-check provider-form-wide">{t("providers.enabled")}<Switch checked={Boolean(form.enabled)} onChange={(enabled) => updateForm("enabled", enabled)} label={t("providers.enabled")} /></label>
            </div>
          )}
          <div className="provider-form-actions">
            <Button variant="ghost" type="button" onClick={() => detectProvider.mutate()} disabled={detectProvider.isPending || !form.base_url.trim()}>{detectProvider.isPending ? t("providers.detecting") : t("providers.detect")}</Button>
            <Button variant="ghost" type="button" onClick={() => setAdvancedOpen((open) => !open)}>{advancedOpen ? t("providers.hideAdvanced") : t("providers.advanced")}</Button>
          </div>
          {probeReport ? <ProviderProbeSuggestionPanel report={probeReport} onApply={applyProbeSuggestions} /> : null}
          {advancedOpen ? (
            <div className="provider-form provider-form-modal">
              {mode === "official" ? <label className="provider-form-wide">{t("providers.baseURL")}<Input value={form.base_url} onChange={(event) => updateForm("base_url", event.target.value)} placeholder={t("providers.baseURLHint")} spellCheck={false} /></label> : null}
              <ProviderAdvancedFields form={form} presetState={customRoutingState} onChange={updateForm} includeHeaders={false} showProtocolSelectors={mode === "official"} />
            </div>
          ) : null}
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>{t("providers.cancel")}</Button>
            <Button variant="primary" type="submit" disabled={createProvider.isPending || !canCreate}>{createProvider.isPending ? t("providers.creating") : t("providers.create")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function createChannelPayload(form, mode, selectedCatalogID) {
  return {
    name: form.name,
    base_url: form.base_url,
    provider_preset: mode === "official" ? selectedCatalogID : "",
    api_type: form.api_type,
    protocol_family: form.protocol_family,
    routing_profile: form.routing_profile,
    api_version: form.api_version,
    deployment: form.deployment,
    project: form.project,
    location: form.location,
    model_resource: form.model_resource,
    api_key: form.api_key,
    enabled: form.enabled,
    priority: Number(form.priority || 0),
    weight: Number(form.weight || 1),
    capacity_hint: Number(form.capacity_hint || 1),
    endpoints: normalizedEndpoints(form.endpoints),
    proxy: channelProxyPayload(form),
    capabilities: capabilitiesFromInterfaces(form.interfaces),
    models: splitModelList(form.models),
    model_discovery: mode === "custom" ? "disabled" : undefined,
  };
}

function ProviderCard({ item, windowValue, onRefresh }) {
  const { t } = useI18n();
  const summary = item.summary || {};
  const [probeOpen, setProbeOpen] = useState(false);
  // Called with the next value only. Bubble suppression lives in the Switch,
  // which is the only place that knows it is nested in a clickable card.
  const setEnabled = useWriteMutation({
    mutationFn: (enabled) => patchJSON(apiPaths.provider(item.id), { enabled }),
    error: "common.actionFailed",
    onSuccess: () => onRefresh?.(),
  });

  const removeProvider = useWriteMutation({
    mutationFn: () => deleteJSON(apiPaths.provider(item.id)),
    error: "common.actionFailed",
    onSuccess: () => onRefresh?.(),
  });

  const deleteProvider = (event) => {
    event.preventDefault();
    event.stopPropagation();
    if (window.confirm(t("providers.deleteConfirm", { name: item.name || item.id }))) {
      removeProvider.mutate();
    }
  };

  const saving = setEnabled.isPending || removeProvider.isPending;
  return (
    <Link className="upstream-card" to={buildProviderLink(item.id, windowValue)}>
      <div className="upstream-card-head">
        <div>
          <p className="eyebrow">{item.plan_name || item.provider_preset || "custom"}</p>
          <h2>{item.name || item.id}</h2>
        </div>
        <div className="trace-tag-group">
          {item.coding_plan ? <InlineTag tone="accent">{t("providers.codingPlan")}</InlineTag> : null}
          <Switch checked={Boolean(item.enabled)} onChange={(enabled) => setEnabled.mutate(enabled)} disabled={saving} label={`${item.name || item.id} enabled`} />
          <Button variant="default" size="icon" type="button" onClick={deleteProvider} disabled={saving} title={t("providers.deleteTitle")} aria-label={t("providers.deleteConfirm", { name: item.name || item.id })}>
            <DeleteIcon />
          </Button>
          <Button 
            variant="ghost" className="provider-card-probe-button"
            type="button"
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              setProbeOpen(true);
            }}
          >
            {t("providers.probe")}
          </Button>
          <InlineTag tone={item.source === "bootstrap" ? "gold" : "green"}>{providerSourceLabel(item.source)}</InlineTag>
          {item.secret_storage_mode ? <InlineTag tone={item.secret_storage_mode === "plaintext-local" ? "gold" : "green"}>{item.secret_storage_mode}</InlineTag> : null}
          {item.last_probe_status ? <InlineTag tone={item.last_probe_status === "success" ? "green" : "danger"}>{item.last_probe_status}</InlineTag> : null}
        </div>
      </div>
      <div className="upstream-meta-grid">
        <Metric label={t("overview.models")} value={`${formatCount(item.enabled_model_count)} / ${formatCount(item.model_count)}`} />
        <Metric label={t("overview.requests")} value={formatCount(summary.request_count)} />
        <Metric label={t("overview.tokens")} value={formatCount(summary.total_tokens)} detail={usageCoverageDetail(summary.missing_usage_request, t)} />
        <Metric label={t("overview.rpm")} value={formatCount(summary.current_rpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_rpm ?? 0) })} />
        <Metric label={t("overview.tpm")} value={formatCount(summary.current_tpm ?? 0)} detail={t("overview.rateAvgDetail", { avg: formatRateAvg(summary.avg_tpm ?? 0) })} />
      </div>
      <p className="trace-subline">{!item.enabled ? t("providers.routingDisabled") : !item.enabled_model_count ? t("providers.chooseModels") : t("providers.routingEnabled")}</p>
      <div className="upstream-card-footer">
        <span className="mono">{item.base_url}</span>
        <span>{formatDateTime(item.last_probe_at || item.updated_at)}</span>
      </div>
      {probeOpen ? <ProviderProbeDialog provider={item} onClose={() => setProbeOpen(false)} onApplied={onRefresh} /> : null}
    </Link>
  );
}

function ProviderProbeDialog({ provider, onClose, onApplied }) {
  const { t } = useI18n();
  const [report, setReport] = useState(null);
  const [applyResult, setApplyResult] = useState(null);
  const providerMap = useMemo(() => new Map([[provider.id, provider]]), [provider]);
  const summary = useMemo(() => summarizeProbeBatchReport(report, providerMap), [report, providerMap]);
  const row = summary.rows[0] || null;

  const previewReport = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providerProbeReport, providerProbeBatchApplyPayload(provider.id)),
    error: "providers.probeFailed",
    onSuccess: (nextReport) => {
      setApplyResult(null);
      setReport(nextReport);
    },
  });

  const applyDetected = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.providerProbeApply, providerProbeBatchApplyPayload(provider.id)),
    error: "providers.probeFailed",
    onSuccess: (result) => {
      setApplyResult(result);
      onApplied?.();
    },
  });

  const busy = previewReport.isPending ? "preview" : applyDetected.isPending ? "apply" : "";

  React.useEffect(() => {
    previewReport();
  }, []);

  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="provider-probe-modal" aria-labelledby="provider-probe-title">
        <DialogHeader>
          <div>
            <p className="eyebrow">{provider.provider_preset || "channel"}</p>
            <DialogTitle id="provider-probe-title">{t("providers.probeTitle")}</DialogTitle>
          </div>
          <DialogClose asChild>
            <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>x</Button>
          </DialogClose>
        </DialogHeader>
        <div className="provider-probe-dialog-summary">
          <Metric label={t("providers.providerFallback")} value={provider.name || provider.id} />
          <Metric label={t("providers.detected")} value={formatCount(summary.detected)} />
          <Metric label={t("providers.applyable")} value={formatCount(summary.applyable.length)} />
        </div>
        {busy === "preview" && !report ? <EmptyState title={t("providers.runningProbe")} detail={t("providers.runningProbeDetail")} compact /> : null}
        {row ? <ProviderProbeBatchRow row={row} /> : null}
        {report && !row ? <EmptyState title={t("providers.noProbe")} detail={t("providers.noProbeDetail")} compact /> : null}
        {applyResult ? <p className="trace-subline">{t("providers.applyAccepted", { result: formatProviderProbeApplyResult(applyResult, t) })}</p> : null}
        <DialogFooter>
          <Button variant="ghost" type="button" onClick={() => previewReport.mutate()} disabled={busy === "preview"}>{busy === "preview" ? t("providers.probing") : t("providers.runAgain")}</Button>
          <Button variant="primary" type="button" onClick={() => applyDetected.mutate()} disabled={busy === "apply" || !summary.applyable.length}>{busy === "apply" ? t("providers.applying") : t("providers.applySuggestions")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
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

function buildProviderTrendItems(items) {
  const times = [];
  const byTime = new Map();
  for (const provider of items) {
    for (const trend of provider.trends || []) {
      const key = trend.time;
      if (!byTime.has(key)) {
        times.push(key);
        byTime.set(key, { time: key, series: {} });
      }
      byTime.get(key).series[provider.id] = trend;
    }
  }
  times.sort();
  return times.map((time) => byTime.get(time));
}

function Metric({ label, value, detail = "" }) {
  return (
    <span className="detail-meta-pill">
      <span className="detail-meta-label">{label}</span>
      <strong>{value}</strong>
      {detail ? <small>{detail}</small> : null}
    </span>
  );
}

const ENDPOINT_PROTOCOLS = ["chat_completions", "responses", "messages", "gemini"];

export function ProviderAdvancedFields({ form, presetState, onChange, includeHeaders = false, showProtocolSelectors = true }) {
  const { t } = useI18n();
  return (
    <>
      {showProtocolSelectors ? (
        <>
          <label>{t("providers.apiType")}
            <Select value={form.api_type || "chat_completions"} onValueChange={(next) => onChange("api_type", next)}>
              <SelectTrigger aria-label={t("providers.apiType")} className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {API_TYPE_OPTIONS.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}
              </SelectContent>
            </Select>
          </label>
          <label>{t("providers.protocol")}
            <Select value={form.protocol_family || ""} onValueChange={(next) => onChange("protocol_family", next)}>
              <SelectTrigger aria-label={t("providers.protocol")} className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {presetState.protocolOptions.map((item) => <SelectItem key={item} value={item}>{item}</SelectItem>)}
              </SelectContent>
            </Select>
          </label>
        </>
      ) : null}
      <label>{t("providers.routingProfile")}
        <Select value={form.routing_profile || ""} onValueChange={(next) => onChange("routing_profile", next)}>
          <SelectTrigger aria-label={t("providers.routingProfile")} className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            {presetState.routingOptions.map((item) => <SelectItem key={item} value={item}>{item}</SelectItem>)}
          </SelectContent>
        </Select>
      </label>
      {presetState.needsAPIVersion ? <label>{t("providers.apiVersion")}<Input value={form.api_version || ""} onChange={(event) => onChange("api_version", event.target.value)} placeholder={presetState.apiVersionPlaceholder} /></label> : null}
      {presetState.needsDeployment ? <label>{t("providers.deployment")}<Input value={form.deployment || ""} onChange={(event) => onChange("deployment", event.target.value)} placeholder="gpt-4o-mini" /></label> : null}
      {presetState.needsProject ? <label>{t("providers.project")}<Input value={form.project || ""} onChange={(event) => onChange("project", event.target.value)} placeholder="my-gcp-project" /></label> : null}
      {presetState.needsLocation ? <label>{t("providers.location")}<Input value={form.location || ""} onChange={(event) => onChange("location", event.target.value)} placeholder="us-central1" /></label> : null}
      {presetState.needsModelResource ? <label className="provider-form-wide">{t("providers.modelResource")}<Input value={form.model_resource || ""} onChange={(event) => onChange("model_resource", event.target.value)} placeholder="publishers/google/models/gemini-2.5-flash" /></label> : null}
      <p className="trace-subline provider-form-wide">{t("providers.endpointsHint")}</p>
      {ENDPOINT_PROTOCOLS.map((protocol) => (
        <label key={protocol}>{t(`providers.endpoint.${protocol}`)}
          <Input value={form.endpoints?.[protocol] || ""} onChange={(event) => onChange("endpoints", { ...(form.endpoints || {}), [protocol]: event.target.value })} placeholder="https://..." spellCheck={false} />
        </label>
      ))}
      <label>{t("providers.priority")}<Input type="number" value={form.priority} onChange={(event) => onChange("priority", event.target.value)} /></label>
      <label>{t("providers.weight")}<Input type="number" step="0.1" value={form.weight} onChange={(event) => onChange("weight", event.target.value)} /></label>
      <label>{t("providers.capacity")}<Input type="number" step="0.1" value={form.capacity_hint} onChange={(event) => onChange("capacity_hint", event.target.value)} /></label>
      <label className="provider-form-check provider-form-wide">{t("providers.networkProxy")}<Switch checked={Boolean(form.proxy_enabled)} onChange={(enabled) => onChange("proxy_enabled", enabled)} label={t("providers.networkProxy")} /></label>
      <p className="trace-subline provider-form-wide">{t("providers.networkProxyHint")}</p>
      {form.proxy_enabled ? (
        <>
          <label>{t("providers.proxyHTTP")}<Input value={form.proxy_http || ""} onChange={(event) => onChange("proxy_http", event.target.value)} placeholder="http://127.0.0.1:7890" spellCheck={false} /></label>
          <label>{t("providers.proxyHTTPS")}<Input value={form.proxy_https || ""} onChange={(event) => onChange("proxy_https", event.target.value)} placeholder={form.proxy_http || "http://127.0.0.1:7890"} spellCheck={false} /></label>
          <label className="provider-form-wide">{t("providers.proxyNoProxy")}<Input value={form.proxy_no_proxy || ""} onChange={(event) => onChange("proxy_no_proxy", event.target.value)} placeholder="localhost, .internal.example.com, 10.0.0.0/8" spellCheck={false} /></label>
        </>
      ) : null}
      {includeHeaders ? <label className="provider-form-wide">{t("providers.headers")}<textarea value={form.headers_text} onChange={(event) => onChange("headers_text", event.target.value)} spellCheck={false} /></label> : null}
    </>
  );
}

function ProviderProbeSuggestionPanel({ report, onApply }) {
  const { t } = useI18n();
  const capabilities = Array.isArray(report.capabilities) ? report.capabilities : [];
  const warnings = Array.isArray(report.warnings) ? report.warnings : [];
  return (
    <div className="provider-probe-card">
      <div className="provider-probe-card-head">
        <div>
          <p className="eyebrow">{t("providers.detection")}</p>
          <h3>{t("providers.probeSuggestions")}</h3>
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
        <Button variant="primary" type="button" onClick={onApply} disabled={report.status !== "detected"}>{t("providers.applySuggestions")}</Button>
      </div>
    </div>
  );
}

const API_TYPE_OPTIONS = [
  { value: "chat_completions", label: "Chat Completions" },
  { value: "responses", label: "Responses" },
  { value: "responses_native", label: "Responses native" },
  { value: "messages", label: "Anthropic messages" },
  { value: "gemini_generate_content", label: "Gemini generateContent" },
];

const SETUP_VALIDATION_FIELDS = new Set([
  "name",
  "base_url",
  "provider_preset",
  "api_type",
  "protocol_family",
  "routing_profile",
  "api_version",
  "deployment",
  "project",
  "location",
  "model_resource",
  "api_key",
  "enabled",
  "priority",
  "weight",
  "capacity_hint",
  "proxy_enabled",
  "proxy_http",
  "proxy_https",
  "proxy_no_proxy",
  "endpoints",
]);

export function buildPresetState(presetData, providerPreset, routingProfile) {
  const presets = Array.isArray(presetData?.presets) ? presetData.presets : [];
  const byID = new Map(presets.map((item) => [item.id, item]));
  const fallbackOptions = ["openai", "openrouter", "anthropic", "google_genai", "azure_openai", "vertex", "vllm"];
  const options = (Array.isArray(presetData?.items) && presetData.items.length ? presetData.items : fallbackOptions).slice().sort();
  const labels = Object.fromEntries((Array.isArray(presetData?.presets) ? presetData.presets : []).map((item) => [item.id, item.name || item.id]));
  const spec = byID.get(providerPreset) || {};
  const defaultProtocolOptions = presetData?.defaults?.protocol_families || ["openai_compatible", "anthropic_messages", "google_genai", "vertex_native"];
  const defaultRoutingOptions = presetData?.defaults?.routing_profiles || ["openai_default", "azure_openai_v1", "azure_openai_deployment", "vllm_openai", "anthropic_default", "google_ai_studio", "vertex_express", "vertex_project_location"];
  const protocolOptions = uniqueSorted([spec.protocol_family, ...defaultProtocolOptions].filter(Boolean));
  const allowedProfiles = Array.isArray(spec.allowed_profiles) && spec.allowed_profiles.length ? spec.allowed_profiles : defaultRoutingOptions;
  const routingOptions = uniqueSorted([routingProfile, spec.routing_profile, ...allowedProfiles].filter(Boolean));
  const effectiveProfile = routingProfile || spec.routing_profile || routingOptions[0] || "";
  return {
    options,
    labels,
    spec,
    protocolOptions,
    routingOptions,
    needsAPIVersion: effectiveProfile.startsWith("azure_openai") || spec.protocol_family === "anthropic_messages",
    needsDeployment: effectiveProfile === "azure_openai_deployment",
    needsProject: effectiveProfile === "vertex_project_location",
    needsLocation: effectiveProfile === "vertex_project_location",
    needsModelResource: effectiveProfile === "vertex_express" || effectiveProfile === "vertex_project_location",
    apiVersionPlaceholder: spec.protocol_family === "anthropic_messages" ? "2023-06-01" : "preview",
  };
}

export function normalizePresetSelection(form, presetData, changedKey) {
  if (changedKey !== "provider_preset") {
    return form;
  }
  const spec = (presetData?.presets || []).find((item) => item.id === form.provider_preset) || {};
  const allowedProfiles = Array.isArray(spec.allowed_profiles) ? spec.allowed_profiles : [];
  const protocolFamily = spec.protocol_family || form.protocol_family || "";
  const apiType = defaultAPITypeForProtocolFamily(protocolFamily);
  const endpoint = presetEndpointForAPIType(spec.endpoints, apiType);
  return {
    ...form,
    api_type: apiType,
    protocol_family: protocolFamily,
    routing_profile: spec.routing_profile || allowedProfiles[0] || form.routing_profile || "",
    // A catalog-matched preset carries the per-protocol endpoint. Filling it
    // here keeps the create form one click, while an unmatched/custom preset
    // leaves the operator's URL untouched.
    base_url: endpoint || form.base_url,
  };
}

function presetEndpointForAPIType(endpoints, apiType) {
  if (!endpoints || typeof endpoints !== "object") {
    return "";
  }
  switch (apiType) {
    case "responses":
    case "responses_native":
      return endpoints.responses || endpoints.chat_completions || "";
    case "messages":
      return endpoints.messages || "";
    case "gemini_generate_content":
      return endpoints.gemini || "";
    default:
      return endpoints.chat_completions || endpoints.responses || "";
  }
}

function defaultAPITypeForProtocolFamily(protocolFamily) {
  switch (protocolFamily) {
    case "anthropic_messages":
      return "messages";
    case "google_genai":
    case "vertex_native":
      return "gemini_generate_content";
    case "openai_compatible":
    default:
      return "chat_completions";
  }
}

function uniqueSorted(values) {
  return Array.from(new Set(values.filter(Boolean))).sort();
}

// The write payload names its fields instead of spreading the form: a payload
// that spread the form would keep re-sending the retired channel-level fields.
function normalizedEndpoints(value) {
  const out = {};
  for (const protocol of ENDPOINT_PROTOCOLS) {
    const endpoint = String(value?.[protocol] || "").trim();
    if (endpoint) {
      out[protocol] = endpoint;
    }
  }
  return out;
}

function channelProxyPayload(form = {}) {
  return {
    enabled: Boolean(form.proxy_enabled),
    http_proxy: String(form.proxy_http || "").trim(),
    https_proxy: String(form.proxy_https || "").trim(),
    no_proxy: String(form.proxy_no_proxy || "").trim(),
  };
}

function providerProbePreviewPayload(form) {
  return {
    provider_id: form.name || form.provider_preset || "new-channel",
    base_url: form.base_url,
    api_key: form.api_key,
    api_type: form.api_type,
    protocol_family: form.protocol_family,
    proxy: channelProxyPayload(form),
  };
}

function providerProbeSuggestionPayload(_form = {}, report = {}) {
  // Only the channel-level API surface is a suggestion now: interface support is
  // detected per model and written to the model rows, not to the channel.
  const payload = {};
  if (report.suggested_api_type) {
    payload.api_type = report.suggested_api_type;
  }
  if (report.suggested_protocol_family) {
    payload.protocol_family = report.suggested_protocol_family;
  }
  return payload;
}

function providerProbeBatchApplyPayload(channelID = "") {
  return channelID ? { channel_id: channelID } : {};
}

function summarizeProbeBatchReport(report, providerMap) {
  const rows = (Array.isArray(report?.reports) ? report.reports : []).map((item) => buildProbeBatchRow(item, providerMap));
  return rows.reduce(
    (state, row) => {
      if (row.status === "detected") {
        state.detected += 1;
      } else if (row.status === "error") {
        state.errors += 1;
      } else {
        state.unknown += 1;
      }
      if (row.status === "detected" && row.fillableCount > 0) {
        state.applyable.push(row);
      }
      state.rows.push(row);
      return state;
    },
    { rows: [], detected: 0, unknown: 0, errors: 0, applyable: [] },
  );
}

function buildProbeBatchRow(report, providerMap) {
  const providerID = report.provider_id || "";
  const provider = providerMap.get(providerID) || {};
  const capabilities = Array.isArray(report.capabilities) ? report.capabilities : [];
  const currentCapabilities = normalizeCapabilities(provider.capabilities || {});
  const fillableCapabilities = capabilities.filter((capability) => BATCH_APPLY_CAPABILITIES.has(capability) && currentCapabilities[capability] === undefined);
  const currentAPIType = provider.api_type || report.specified_api_type || "";
  const currentProtocolFamily = provider.protocol_family || report.specified_protocol_family || "";
  const suggestedAPIType = report.suggested_api_type || "";
  const suggestedProtocolFamily = report.suggested_protocol_family || "";
  const apiTypeFillable = Boolean(suggestedAPIType && !currentAPIType);
  const protocolFillable = Boolean(suggestedProtocolFamily && !currentProtocolFamily);
  const fillableCount = Number(apiTypeFillable) + Number(protocolFillable) + fillableCapabilities.length;
  return {
    providerID,
    providerName: provider.name || "",
    baseURL: report.base_url || provider.base_url || "",
    status: report.status || "unknown",
    warnings: Array.isArray(report.warnings) ? report.warnings : [],
    capabilities,
    fillableCapabilities,
    fillableCount,
    currentAPIType,
    currentProtocolFamily,
    suggestedAPIType,
    suggestedProtocolFamily,
    apiTypeFillable,
    protocolFillable,
  };
}

const BATCH_APPLY_CAPABILITIES = new Set(["responses", "chat_completions", "tool_calling", "models", "embeddings", "tokenize"]);

function formatProviderProbeApplyResult(result = {}, t = (key) => key) {
  const applied = Array.isArray(result.applied) ? result.applied : [];
  if (applied.length) {
    const updated = applied.filter((item) => item.applied).length;
    return t("providers.appliedSkipped", { applied: formatCount(updated), skipped: formatCount(applied.length - updated) });
  }
  if (result.status) {
    return result.status;
  }
  return t("providers.backendReceived");
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

function summarizeProviders(items) {
  return items.reduce(
    (state, item) => {
      const summary = item.summary || {};
      if (item.enabled) {
        state.enabled += 1;
      }
      state.requests += Number(summary.request_count || 0);
      state.missing += Number(summary.missing_usage_request || 0);
      state.tokens += Number(summary.total_tokens || 0);
      return state;
    },
    { enabled: 0, requests: 0, tokens: 0, missing: 0 },
  );
}

function usageCoverageDetail(missing, t = (key, values) => `${values?.count || 0} missing usage`) {
  const count = Number(missing || 0);
  return count > 0 ? t("providers.missingUsage", { count: formatCount(count) }) : "";
}
