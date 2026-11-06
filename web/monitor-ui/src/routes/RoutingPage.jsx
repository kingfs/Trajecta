import React, { useEffect, useState } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Input } from "../components/ui/input";
import { Button } from "../components/ui/button";
import { toast } from "sonner";
import { EmptyState } from "../components/common/EmptyState";
import { InlineTag } from "../components/common/Badges";
import { useJSON } from "../hooks/useJSON";
import { useRefresh } from "../hooks/useRefresh";
import { apiPaths, patchJSON, postJSON, requestJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { formatCount } from "../lib/monitor";

/*
 * The routing controls that outlived the standalone /routing page.
 *
 * The page itself is gone. Its decision list and credential summary duplicated
 * what a trace's own request tab already shows, so they were dropped and the
 * trace remains the source for "which upstream served this call". Three
 * controls had no other home, so they are exported as panels: the channel page
 * embeds the settings form and the dry-run inspector, and the model page embeds
 * the alias table.
 *
 * Each panel renders content only. The caller owns the card shell - both pages
 * put the panel inside a collapsed-by-default collapsible card - so embedding
 * them does not nest one card inside another, and the panel is mounted only
 * while the card is open, which is also why a reader who never opens it never
 * pays for its reads. The APIs behind all three are unchanged.
 */

export function RoutingSettingsPanel() {
  const { t } = useI18n();
  const [settings, setSettings] = useState({ responses_strategy: "auto", selection_policy: "p2c" });
  // The loading flag and its error belong to the read that fills the form; the
  // save reports itself through a toast like every other write here.
  const [loadingSettings, setLoadingSettings] = useState(true);
  const [loadError, setLoadError] = useState("");

  useEffect(() => {
    let cancelled = false;
    requestJSON(apiPaths.routingSettings)
      .then((payload) => {
        if (!cancelled) {
          setSettings({ ...settings, ...payload });
          setLoadingSettings(false);
          setLoadError("");
        }
      })
      .catch((error) => {
        if (!cancelled) {
          setLoadingSettings(false);
          setLoadError(error.message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const update = (key, value) => setSettings((current) => ({ ...current, [key]: value }));
  // The server's copy wins over the one that was submitted: it is the stored
  // settings, not the echoed request.
  const save = useWriteMutation({
    mutationFn: () => patchJSON(apiPaths.routingSettings, settings),
    success: "routing.saved",
    error: "routing.settingsUnavailable",
    onSuccess: (payload) => setSettings((current) => ({ ...current, ...payload })),
  });

  return (
    <>
      {loadError ? <EmptyState title={t("routing.settingsUnavailable")} detail={loadError} compact /> : null}
      <form className="filter-bar routing-filter-bar" onSubmit={(event) => { event.preventDefault(); save.mutate(); }}>
        <label className="filter-label">
          {t("routing.responsesStrategy")}
          <Select value={settings.responses_strategy || "auto"} onValueChange={(next) => update("responses_strategy", next)}>
            <SelectTrigger aria-label={t("routing.responsesStrategy")} className="min-w-[180px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">{t("routing.strategyAuto")}</SelectItem>
              <SelectItem value="prefer_native">{t("routing.strategyPreferNative")}</SelectItem>
              <SelectItem value="prefer_local_server">{t("routing.strategyPreferLocalServer")}</SelectItem>
              <SelectItem value="native_only">{t("routing.strategyNativeOnly")}</SelectItem>
              <SelectItem value="local_server_only">{t("routing.strategyLocalServerOnly")}</SelectItem>
            </SelectContent>
          </Select>
        </label>
        <label className="filter-label">
          {t("routing.selectionPolicy")}
          <Select value={settings.selection_policy || "p2c"} onValueChange={(next) => update("selection_policy", next)}>
            <SelectTrigger aria-label={t("routing.selectionPolicy")} className="min-w-[180px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="p2c">p2c</SelectItem>
              <SelectItem value="first_available">first_available</SelectItem>
            </SelectContent>
          </Select>
        </label>
        <Button variant="ghost" type="submit" disabled={loadingSettings}>{t("common.save")}</Button>
        {save.isPending ? <InlineTag tone="gold">{t("common.saving")}</InlineTag> : null}
      </form>
    </>
  );
}

export function ModelAliasesPanel() {
  const refresh = useRefresh();
  const { t } = useI18n();
  const aliases = useJSON(apiPaths.modelAliases, []);
  const [form, setForm] = useState({ alias: "", target_model: "", channel_id: "" });
  const [validation, setValidation] = useState(emptyAliasValidationState());
  const [editID, setEditID] = useState("");
  const [editForm, setEditForm] = useState(null);
  const [editValidation, setEditValidation] = useState(emptyAliasValidationState());
  const items = Array.isArray(aliases.data?.items) ? aliases.data.items : [];
  const update = (key, value) => setForm((current) => ({ ...current, [key]: value }));
  const updateEdit = (key, value) => setEditForm((current) => ({ ...current, [key]: value }));

  useEffect(() => {
    let cancelled = false;
    if (!shouldValidateAlias(form)) {
      setValidation(emptyAliasValidationState());
      return () => {
        cancelled = true;
      };
    }
    setValidation((current) => ({ ...current, loading: true, error: "" }));
    const timer = window.setTimeout(() => {
      validateAlias(form)
        .then((data) => {
          if (!cancelled) {
            setValidation({ loading: false, data, error: "" });
          }
        })
        .catch((error) => {
          if (!cancelled) {
            setValidation({ loading: false, data: null, error: error.message });
          }
        });
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [form]);

  useEffect(() => {
    let cancelled = false;
    if (!editID || !editForm || !shouldValidateAlias(editForm)) {
      setEditValidation(emptyAliasValidationState());
      return () => {
        cancelled = true;
      };
    }
    setEditValidation((current) => ({ ...current, loading: true, error: "" }));
    const timer = window.setTimeout(() => {
      validateAlias(editForm)
        .then((data) => {
          if (!cancelled) {
            setEditValidation({ loading: false, data, error: "" });
          }
        })
        .catch((error) => {
          if (!cancelled) {
            setEditValidation({ loading: false, data: null, error: error.message });
          }
        });
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [editID, editForm]);

  // Validation runs first and is not part of the write: a form that fails it is
  // never posted, so the warning is raised here rather than by the mutation.
  const createAlias = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.modelAliases, form),
    error: "routing.aliasSaveFailed",
    onSuccess: () => {
      setForm({ alias: "", target_model: "", channel_id: "" });
      setValidation(emptyAliasValidationState());
      refresh();
    },
  });

  const create = async (event) => {
    event.preventDefault();
    try {
      const checked = await validateAlias(form);
      setValidation({ loading: false, data: checked, error: "" });
      if (!checked.valid) {
        toast.warning(t("routing.resolveValidationErrors"));
        return;
      }
      createAlias.mutate();
    } catch (error) {
      toast.error(error.message);
    }
  };
  const startEdit = (item) => {
    setEditID(item.id || "");
    setEditForm({
      id: item.id || "",
      alias: item.alias || "",
      target_model: item.target_model || "",
      channel_id: item.channel_id || "",
      enabled: item.enabled !== false,
      description: item.description || "",
      source: item.source || "",
    });
  };
  const cancelEdit = () => {
    setEditID("");
    setEditForm(null);
    setEditValidation(emptyAliasValidationState());
  };
  const saveAlias = useWriteMutation({
    mutationFn: () => patchJSON(apiPaths.modelAlias(editID), editForm),
    error: "routing.aliasSaveFailed",
    onSuccess: () => {
      cancelEdit();
      refresh();
    },
  });

  const saveEdit = async (event) => {
    event.preventDefault();
    try {
      const checked = await validateAlias(editForm);
      setEditValidation({ loading: false, data: checked, error: "" });
      if (!checked.valid) {
        toast.warning(t("routing.resolveValidationErrors"));
        return;
      }
      saveAlias.mutate();
    } catch (error) {
      toast.error(error.message);
    }
  };
  const createDisabled = isAliasSaveDisabled(form, validation);
  const editDisabled = isAliasSaveDisabled(editForm || {}, editValidation);

  return (
    <>
      <div className="trace-tag-group">
        <InlineTag>{t("routing.aliasCount", { count: formatCount(items.length) })}</InlineTag>
      </div>
      {aliases.error ? <EmptyState title={t("routing.aliasesUnavailable")} detail={aliases.error} compact /> : null}
      <form className="filter-bar routing-filter-bar" onSubmit={create}>
        <Input className="min-w-[180px]" placeholder={t("routing.aliasPlaceholder")} value={form.alias} onChange={(event) => update("alias", event.target.value)} />
        <Input className="min-w-[180px]" placeholder={t("routing.targetModelPlaceholder")} value={form.target_model} onChange={(event) => update("target_model", event.target.value)} />
        <Input className="min-w-[180px]" placeholder={t("routing.optionalChannel")} value={form.channel_id} onChange={(event) => update("channel_id", event.target.value)} />
        <Button variant="ghost" type="submit" disabled={createDisabled}>{t("routing.create")}</Button>
      </form>
      <AliasValidationMessages state={validation} />
      {items.length ? (
        <div className="session-breakdown-grid">
          {items.map((item) => (
            <section className="breakdown-card" key={item.id || `${item.alias}:${item.channel_id}:${item.target_model}`}>
              {editID === item.id && editForm ? (
                <form className="routing-summary-stack" onSubmit={saveEdit}>
                  <div className="breakdown-title">{t("routing.editAlias")}</div>
                  <Input className="min-w-[180px]" placeholder={t("routing.alias")} value={editForm.alias} onChange={(event) => updateEdit("alias", event.target.value)} />
                  <Input className="min-w-[180px]" placeholder={t("routing.targetModel")} value={editForm.target_model} onChange={(event) => updateEdit("target_model", event.target.value)} />
                  <Input className="min-w-[180px]" placeholder={t("routing.optionalChannel")} value={editForm.channel_id} onChange={(event) => updateEdit("channel_id", event.target.value)} />
                  <label className="checkbox-row"><input type="checkbox" checked={editForm.enabled !== false} onChange={(event) => updateEdit("enabled", event.target.checked)} /> {t("routing.enabled")}</label>
                  <AliasValidationMessages state={editValidation} />
                  <div className="trace-tag-group">
                    <Button variant="ghost" type="submit" disabled={editDisabled}>{t("common.save")}</Button>
                    <Button variant="ghost" type="button" onClick={cancelEdit}>{t("providers.cancel")}</Button>
                  </div>
                </form>
              ) : (
                <div className="routing-summary-stack">
                  <div className="breakdown-title">{item.channel_id || t("routing.global")}</div>
                  <strong className="trace-model-name">{t("routing.aliasTo", { alias: item.alias, target: item.target_model })}</strong>
                  <div className="trace-tag-group">
                    <InlineTag tone={item.enabled === false ? "gold" : "green"}>{item.enabled === false ? t("routing.disabled") : t("routing.enabled")}</InlineTag>
                    {item.source ? <InlineTag>{item.source}</InlineTag> : null}
                  </div>
                  {item.description ? <span className="trace-subline">{item.description}</span> : null}
                  <Button variant="ghost" type="button" onClick={() => startEdit(item)}>{t("routing.edit")}</Button>
                </div>
              )}
            </section>
          ))}
        </div>
      ) : !aliases.error ? <EmptyState title={t("routing.noAliases")} detail={t("routing.noAliasesDetail")} compact /> : null}
    </>
  );
}

export function RouteInspectorPanel() {
  const { t } = useI18n();
  const [form, setForm] = useState({ endpoint: "responses", model: "", stream: false, tools: false });
  const [result, setResult] = useState(null);
  const update = (key, value) => setForm((current) => ({ ...current, [key]: value }));
  const inspect = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.routingInspect, form),
    error: "routing.inspectorUnavailable",
    onSuccess: setResult,
  });
  const runInspect = (event) => {
    event.preventDefault();
    setResult(null);
    inspect.mutate();
  };

  return (
    <>
      <form className="filter-bar routing-filter-bar" onSubmit={runInspect}>
        <Select value={form.endpoint} onValueChange={(next) => update("endpoint", next)}>
          <SelectTrigger aria-label={t("routing.endpoint")} className="min-w-[180px]"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="chat_completions">chat_completions</SelectItem>
            <SelectItem value="responses">responses</SelectItem>
            <SelectItem value="anthropic_messages">anthropic_messages</SelectItem>
          </SelectContent>
        </Select>
        <Input className="min-w-[180px]" placeholder={t("routing.model")} value={form.model} onChange={(event) => update("model", event.target.value)} />
        <label className="checkbox-row"><input type="checkbox" checked={form.stream} onChange={(event) => update("stream", event.target.checked)} /> {t("routing.stream")}</label>
        <label className="checkbox-row"><input type="checkbox" checked={form.tools} onChange={(event) => update("tools", event.target.checked)} /> {t("routing.tools")}</label>
        <Button variant="ghost" type="submit" disabled={inspect.isPending}>{t("routing.inspect")}</Button>
      </form>
      {result ? <RouteInspectorResult result={result} /> : null}
      {/* The failure is reported by the toast the write already raises, so the
          hint stands only while there is nothing else to read. `error` here used
          to be an undefined identifier, which took the whole route down through
          the error boundary the moment the panel was opened. */}
      {!result && !inspect.isError ? <EmptyState title={t("routing.noDryRun")} detail={t("routing.noDryRunDetail")} compact /> : null}
    </>
  );
}

function RouteInspectorResult({ result }) {
  const { t } = useI18n();
  const plan = result?.result?.plan || {};
  const candidates = Array.isArray(result?.result?.candidates) ? result.result.candidates : [];
  const hasPlan = Boolean(plan.execution_mode || plan.selected_candidate_id || plan.upstream_endpoint || plan.upstream_model);
  return (
    <div className="routing-summary-stack">
      {result.error ? <EmptyState title={t("routing.noExecutableRoute")} detail={result.error} tone="danger" compact /> : null}
      {hasPlan ? (
        <div className="session-breakdown-grid">
          <section className="breakdown-card">
            <div className="breakdown-title">{t("routing.execution")}</div>
            <strong className="trace-model-name">{formatInspectValue(plan.execution_mode)}</strong>
            <span className="trace-subline">{formatInspectValue(plan.reason || t("routing.selected"))}</span>
          </section>
          <section className="breakdown-card">
            <div className="breakdown-title">{t("routing.upstream")}</div>
            <strong className="trace-model-name">{formatInspectValue(plan.upstream_endpoint)}</strong>
            <span className="trace-subline mono">{formatInspectValue(plan.upstream_model)}</span>
          </section>
          <section className="breakdown-card">
            <div className="breakdown-title">{t("routing.selectedTarget")}</div>
            <strong className="trace-model-name">{formatInspectValue(plan.selected_route_target_id || plan.selected_candidate_id)}</strong>
            <span className="trace-subline mono">{t("routing.channelLabel", { value: formatInspectValue(plan.selected_channel_id) })}</span>
          </section>
          <section className="breakdown-card">
            <div className="breakdown-title">{t("routing.requestModel")}</div>
            <strong className="trace-model-name">{formatInspectValue(plan.requested_model || result?.request?.model)}</strong>
            <span className="trace-subline">{t("routing.resolvedCandidates", { count: formatCount(plan.resolved_model_candidates?.length || 0) })}</span>
          </section>
        </div>
      ) : null}
      {plan.resolved_model_candidates?.length ? (
        <section className="breakdown-card">
          <div className="breakdown-title">{t("routing.resolvedModels")}</div>
          <div className="routing-candidate-list">
            {plan.resolved_model_candidates.map((candidate, index) => (
              <article className="routing-candidate-card routing-candidate-card-active" key={`${candidate.channel_id || "global"}-${candidate.model}-${index}`}>
                <div className="routing-candidate-head">
                  <div>
                    <strong>{candidate.model || t("routing.unknownModel")}</strong>
                    <span className="trace-subline mono">{candidate.alias ? t("routing.aliasPrefix", { alias: candidate.alias }) : candidate.source || "request"}</span>
                  </div>
                  <div className="trace-tag-group">
                    {candidate.channel_id ? <InlineTag tone="accent">{candidate.channel_id}</InlineTag> : <InlineTag>{t("routing.global")}</InlineTag>}
                    {candidate.source ? <InlineTag>{candidate.source}</InlineTag> : null}
                  </div>
                </div>
              </article>
            ))}
          </div>
        </section>
      ) : null}
      {candidates.length ? <RouteInspectorCandidates candidates={candidates} selectedID={plan.selected_candidate_id} /> : null}
      <details className="breakdown-card">
        <summary className="breakdown-title">{t("routing.rawInspectorResponse")}</summary>
        <pre className="trace-json-block">{JSON.stringify(result, null, 2)}</pre>
      </details>
    </div>
  );
}

function RouteInspectorCandidates({ candidates, selectedID }) {
  const { t } = useI18n();
  return (
    <section className="breakdown-card">
      <div className="breakdown-title">{t("routing.candidateReasons")}</div>
      <div className="routing-candidate-list">
        {candidates.map((candidate, index) => {
          const selected = candidate.selectable && candidate.candidate_id === selectedID;
          return (
            <article className={candidate.selectable ? "routing-candidate-card routing-candidate-card-active" : "routing-candidate-card"} key={`${candidate.candidate_id || "candidate"}-${candidate.execution_mode}-${candidate.upstream_endpoint}-${index}`}>
              <div className="routing-candidate-head">
                <div>
                  <strong>{candidate.route_target_id || candidate.candidate_id || t("routing.unknownTarget")}</strong>
                  <span className="trace-subline mono">{t("routing.viaEndpoint", { mode: formatInspectValue(candidate.execution_mode), endpoint: formatInspectValue(candidate.upstream_endpoint) })}</span>
                </div>
                <div className="trace-tag-group">
                  <InlineTag tone={candidate.selectable ? "green" : "gold"}>{selected ? t("routing.selected") : candidate.selectable ? t("routing.selectable") : t("routing.filtered")}</InlineTag>
                  <InlineTag>{formatInspectValue(candidate.reason)}</InlineTag>
                  {candidate.channel_id ? <InlineTag tone="accent">{candidate.channel_id}</InlineTag> : null}
                </div>
              </div>
              <div className="detail-meta-strip">
                <DetailMeta label={t("routing.model")} value={candidate.upstream_model || "-"} mono />
                <DetailMeta label={t("routing.rank")} value={candidate.rank ?? "-"} />
                <DetailMeta label={t("routing.candidate")} value={candidate.candidate_id || "-"} mono />
              </div>
            </article>
          );
        })}
      </div>
    </section>
  );
}

function DetailMeta({ label, value, mono = false }) {
  return (
    <span className={mono ? "mono" : ""}>
      <small>{label}</small>
      <strong>{value}</strong>
    </span>
  );
}

function AliasValidationMessages({ state }) {
  const { t } = useI18n();
  const data = state?.data;
  const errors = Array.isArray(data?.errors) ? data.errors : [];
  const warnings = Array.isArray(data?.warnings) ? data.warnings : [];
  if (!state?.loading && !state?.error && !errors.length && !warnings.length) {
    return null;
  }
  return (
    <div className="routing-summary-stack">
      {state.loading ? <p className="trace-subline">{t("routing.validatingAlias")}</p> : null}
      {state.error ? <p className="event-message">{t("routing.validationUnavailable", { error: state.error })}</p> : null}
      {errors.map((error) => <p className="event-message" key={error}>{error}</p>)}
      {warnings.map((warning) => (
        <p className="trace-subline" key={`${warning.code}:${warning.message}`}>
          {t("routing.validationWarning", { code: warning.code, message: warning.message })}
        </p>
      ))}
    </div>
  );
}

function emptyAliasValidationState() {
  return { loading: false, data: null, error: "" };
}

function shouldValidateAlias(form = {}) {
  return Boolean(String(form.alias || "").trim() || String(form.target_model || "").trim() || String(form.channel_id || "").trim());
}

async function validateAlias(form = {}) {
  return postJSON(apiPaths.modelAliasValidate, {
    id: form.id || "",
    alias: form.alias || "",
    target_model: form.target_model || "",
    channel_id: form.channel_id || "",
    enabled: form.enabled,
    description: form.description || "",
    source: form.source || "",
  });
}

function isAliasSaveDisabled(form = {}, validation = emptyAliasValidationState()) {
  if (!String(form.alias || "").trim() || !String(form.target_model || "").trim()) {
    return true;
  }
  if (validation.loading || validation.error) {
    return true;
  }
  return validation.data?.valid === false;
}

function formatInspectValue(value) {
  return value === undefined || value === null || value === "" ? "-" : String(value);
}
