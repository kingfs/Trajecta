import React, { useEffect, useState } from "react";
import { EmptyState } from "../../components/common/EmptyState";
import { DetailMetaPill, InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";

export function ToolsPanel() {
  const { t } = useI18n();
  const state = useJSON(apiPaths.responsesFunctionExecutors, []);
  const [localSummary, setLocalSummary] = useState(null);
  const [enabledDraft, setEnabledDraft] = useState(false);
  const [writeState, setWriteState] = useState({ loading: false, message: "", error: "" });
  const data = localSummary || state.data || {};
  const executors = data.executors || [];
  const warnings = data.warnings || [];

  useEffect(() => {
    if (state.data) {
      setLocalSummary(null);
      setEnabledDraft(Boolean(state.data.enabled));
      setWriteState({ loading: false, message: "", error: "" });
    }
  }, [state.data]);

  const submitExecutorConfig = (validateOnly) => {
    setWriteState({ loading: true, message: "", error: "" });
    requestJSON(apiPaths.responsesFunctionExecutors, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ validate_only: validateOnly, enabled: enabledDraft }),
    })
      .then((payload) => {
        if (!payload.validate_only && payload.summary) {
          setLocalSummary(payload.summary);
        }
        setWriteState({ loading: false, message: payload.validate_only ? t("audit.validationPassed") : t("audit.applyToProcess"), error: "" });
      })
      .catch((error) => {
        setWriteState({ loading: false, message: "", error: error.message || t("audit.updateExecutorsError") });
      });
  };

  return (
    <>
      <section className="panel responses-function-executors-panel">
        <div className="panel-head">
          <div>
            <h2>{t("audit.serverTools")}</h2>
            <p className="trace-subline">{t("audit.toolsHint")}</p>
          </div>
          {state.loading && !state.data ? <InlineTag>{t("audit.loading")}</InlineTag> : <InlineTag tone={data.enabled ? "green" : "gold"}>{data.enabled ? t("audit.enabled") : t("audit.disabled")}</InlineTag>}
        </div>
        {state.error ? <EmptyState title={t("audit.loadFunctionExecutorsError")} detail={state.error} tone="danger" compact /> : null}
        {!state.error ? (
          <>
            <div className="responses-function-executor-controls">
              <label className="provider-form-check">
                <input type="checkbox" checked={enabledDraft} onChange={(event) => setEnabledDraft(event.target.checked)} />
                {t("audit.enableServerTools")}
              </label>
              <div className="provider-form-actions">
                <button className="ghost-button" type="button" disabled={writeState.loading} onClick={() => submitExecutorConfig(true)}>{t("audit.validate")}</button>
                <button className="ghost-button active" type="button" disabled={writeState.loading} onClick={() => submitExecutorConfig(false)}>{t("common.apply")}</button>
              </div>
              {writeState.message ? <InlineTag tone="green">{writeState.message}</InlineTag> : null}
              {writeState.error ? <InlineTag tone="danger">{writeState.error}</InlineTag> : null}
            </div>
            <div className="detail-meta-strip">
              <DetailMetaPill label={t("audit.timeout")} value={data.timeout || "-"} />
              <DetailMetaPill label={t("audit.maxResult")} value={data.max_result_bytes ? `${data.max_result_bytes} bytes` : "-"} />
              <DetailMetaPill label={t("audit.arguments")} value={data.redaction?.arguments ? t("audit.redacted") : t("audit.visible")} />
              <DetailMetaPill label={t("audit.output")} value={data.redaction?.output ? t("audit.redacted") : t("audit.visible")} />
            </div>
            <div className="trace-tag-group">
              {(data.supported_types || []).map((type) => <InlineTag key={type} tone="accent">{type}</InlineTag>)}
              {!data.supported_types?.length ? <InlineTag>{t("audit.noSupportedTypes")}</InlineTag> : null}
            </div>
            <ExecutorWarnings title={t("audit.warnings")} warnings={warnings} />
            {executors.length ? (
              <div className="responses-function-executor-list">
                {executors.map((executor) => (
                  <article key={`${executor.name}:${executor.type}`} className="finding-card responses-function-executor-card">
                    <div className="finding-card-head">
                      <div>
                        <strong>{executor.name || t("audit.unnamed")}</strong>
                        <span>{executor.type || "unknown"}</span>
                      </div>
                      <div className="trace-tag-group">
                        <InlineTag tone={executor.enabled ? "green" : "gold"}>{executor.enabled ? t("common.enabled") : t("common.disabled")}</InlineTag>
                        <InlineTag tone={executor.available ? "green" : "danger"}>{executor.available ? t("audit.executorAvailable") : t("audit.executorUnavailable")}</InlineTag>
                        <InlineTag tone={executor.output_configured ? "accent" : "default"}>{executor.output_configured ? t("audit.outputConfigured") : t("audit.noOutput")}</InlineTag>
                        <InlineTag tone={executor.command_configured ? "accent" : "gold"}>{executor.command_configured ? t("audit.commandConfigured") : t("audit.noCommand")}</InlineTag>
                      </div>
                    </div>
                    <div className="detail-meta-strip responses-function-executor-state">
                      <DetailMetaPill label={t("audit.executorAvailable")} value={formatBool(executor.available, t)} />
                      <DetailMetaPill label={t("audit.command")} value={formatBool(executor.command_configured, t)} />
                      <DetailMetaPill label={t("audit.output")} value={formatBool(executor.output_configured, t)} />
                    </div>
                    <ExecutorWarnings title={t("audit.warnings")} warnings={executor.warnings || []} compact />
                  </article>
                ))}
              </div>
            ) : (
              <EmptyState title={t("audit.noFunctionExecutors")} detail={t("audit.noFunctionExecutorsDetail")} compact />
            )}
          </>
        ) : null}
      </section>
    </>
  );
}

function ExecutorWarnings({ title, warnings, compact = false }) {
  const items = Array.isArray(warnings) ? warnings.filter(Boolean) : [];
  if (!items.length) {
    return null;
  }
  return (
    <div className={`responses-function-executor-warnings ${compact ? "responses-function-executor-warnings-compact" : ""}`.trim()}>
      <strong>{title}</strong>
      <ul>
        {items.map((warning, index) => <li key={`${String(warning)}:${index}`}>{String(warning)}</li>)}
      </ul>
    </div>
  );
}

function formatBool(value, t) {
  return value ? t("common.yes") : t("common.no");
}
