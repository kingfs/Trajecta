import React, { useEffect, useState } from "react";
import { Button } from "../../components/ui/button";
import { Link, useSearchParams } from "react-router-dom";
import { EmptyState } from "../../components/common/EmptyState";
import { DetailMetaPill, InlineTag } from "../../components/common/Badges";
import { CodeBlock } from "../../components/common/Display";
import { apiPaths, apiURL, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime, setOrDeleteParam } from "../../lib/monitor";

export function LineagePanel() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const responseID = searchParams.get("response_id") || "";
  const requestAuditID = searchParams.get("request_audit_id") || "";
  const [traceForm, setTraceForm] = useState({ responseID, requestAuditID });
  const [traceState, setTraceState] = useState({ loading: false, data: null, error: "" });

  useEffect(() => {
    setTraceForm({ responseID, requestAuditID });
  }, [responseID, requestAuditID]);

  useEffect(() => {
    if (!responseID && !requestAuditID) {
      setTraceState({ loading: false, data: null, error: "" });
      return undefined;
    }
    let cancelled = false;
    const controller = new AbortController();
    const traceParams = new URLSearchParams();
    if (responseID) {
      traceParams.set("response_id", responseID);
    }
    if (requestAuditID) {
      traceParams.set("request_audit_id", requestAuditID);
    }
    setTraceState((current) => ({ ...current, loading: true, error: "" }));
    requestJSON(apiURL(apiPaths.responsesAuditTrace, traceParams), { signal: controller.signal })
      .then((data) => {
        if (!cancelled) {
          setTraceState({ loading: false, data, error: "" });
        }
      })
      .catch((error) => {
        if (cancelled || error.name === "AbortError") {
          return;
        }
        setTraceState({ loading: false, data: null, error: error.message || t("audit.loadResponsesTraceError") });
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [responseID, requestAuditID, t]);

  const applyTraceQuery = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "response_id", traceForm.responseID.trim());
    setOrDeleteParam(next, "request_audit_id", traceForm.requestAuditID.trim());
    setSearchParams(next);
  };
  const resetTraceQuery = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("response_id");
    next.delete("request_audit_id");
    setSearchParams(next);
    setTraceForm({ responseID: "", requestAuditID: "" });
  };

  return (
    <>
      <section className="panel responses-audit-panel">
        <div className="panel-head">
          <div>
            <h2>{t("audit.responsesTraceTitle")}</h2>
          </div>
          {traceState.data ? (
            <InlineTag tone={traceState.data.request_audit?.status === "completed" ? "green" : "gold"}>{traceState.data.request_audit?.status || t("lineage.loaded")}</InlineTag>
          ) : null}
        </div>
        <form className="filter-bar responses-audit-query" onSubmit={applyTraceQuery}>
          <input
            className="filter-input"
            type="search"
            placeholder={t("audit.responseId")}
            value={traceForm.responseID}
            onChange={(event) => setTraceForm((current) => ({ ...current, responseID: event.target.value }))}
          />
          <input
            className="filter-input"
            type="search"
            placeholder={t("audit.requestAuditId")}
            value={traceForm.requestAuditID}
            onChange={(event) => setTraceForm((current) => ({ ...current, requestAuditID: event.target.value }))}
          />
          <Button variant="primary" type="submit" disabled={!traceForm.responseID.trim() && !traceForm.requestAuditID.trim()}>{t("audit.loadTrace")}</Button>
          <Button variant="ghost" type="button" onClick={resetTraceQuery}>{t("audit.clear")}</Button>
        </form>
        {!responseID && !requestAuditID ? <EmptyState title={t("audit.noResponsesTrace")} detail={t("audit.noResponsesTraceDetail")} compact /> : null}
        {traceState.loading ? <EmptyState title={t("audit.loadingResponsesTrace")} detail={t("audit.loadingResponsesTraceDetail")} compact /> : null}
        {traceState.error ? <EmptyState title={t("audit.loadResponsesTraceError")} detail={traceState.error} tone="danger" compact /> : null}
        {traceState.data ? <ResponsesAuditTrace trace={traceState.data} /> : null}
      </section>
    </>
  );
}

function ResponsesAuditTrace({ trace }) {
  const { t } = useI18n();
  const audit = trace.request_audit || {};
  const events = trace.events || [];
  const entryExchange = trace.entry_exchange || null;
  const exchanges = trace.model_exchanges?.length ? trace.model_exchanges : (trace.upstream_exchanges || []);
  return (
    <div className="responses-audit-trace">
      <div className="finding-card responses-audit-record">
        <div className="finding-card-head">
          <div>
            <strong>{audit.method || t("lineage.requestFallback")} {audit.path || "/v1/responses"}</strong>
            <span>{audit.id || trace.query?.request_audit_id || "-"}</span>
          </div>
          <div className="trace-tag-group">
            <InlineTag tone={audit.status === "completed" ? "green" : audit.error_text ? "danger" : "gold"}>{audit.status || t("lineage.unknown")}</InlineTag>
            {audit.response_id ? <InlineTag tone="accent">{audit.response_id}</InlineTag> : null}
          </div>
        </div>
        <div className="detail-meta-strip">
          <DetailMetaPill label={t("lineage.created")} value={formatDateTime(audit.created_at)} />
          <DetailMetaPill label={t("lineage.conversation")} value={audit.conversation_id || "-"} mono />
          <DetailMetaPill label={t("lineage.clientRequest")} value={audit.client_request_id || "-"} mono />
          <DetailMetaPill label={t("lineage.bodySha256")} value={audit.body_sha256 || "-"} mono />
        </div>
        {audit.error_text ? <pre className="timeline-message responses-audit-error">{audit.error_text}</pre> : null}
        <div className="responses-audit-json-grid">
          <div>
            <div className="breakdown-title">{t("audit.bodyPreview")}</div>
            <CodeBlock value={audit.body_preview || t("lineage.empty")} />
          </div>
          <div>
            <div className="breakdown-title">{t("audit.headers")}</div>
            <CodeBlock value={formatJSON(audit.header_json, t)} />
          </div>
        </div>
      </div>

      {entryExchange ? <ResponsesEntryExchangeSummary exchange={entryExchange} /> : null}

      <section className="responses-audit-section">
        <div className="panel-head panel-head-compact">
          <div>
            <h2>{t("audit.executionEvents")}</h2>
          </div>
          <InlineTag>{events.length}</InlineTag>
        </div>
        {events.length ? (
          <div className="timeline-list responses-audit-events">
            {events.map((event) => (
              <article key={event.id} className="timeline-item">
                <div className="timeline-rail">
                  <span className={event.status === "failed" || event.status === "rejected" ? "timeline-dot timeline-dot-danger" : "timeline-dot timeline-dot-live"} />
                </div>
                <div className="timeline-card">
                  <div className="timeline-head">
                    <strong>{event.event_type || t("lineage.event")}</strong>
                    <span>{formatDateTime(event.occurred_at)}</span>
                    <span className="timeline-badge">{event.status || event.phase || t("lineage.event")}</span>
                  </div>
                  <div className="detail-meta-strip">
                    <DetailMetaPill label={t("lineage.phase")} value={event.phase || "-"} />
                    <DetailMetaPill label={t("lineage.eventId")} value={event.id || "-"} mono />
                    <DetailMetaPill label={t("lineage.response")} value={event.response_id || "-"} mono />
                  </div>
                  {event.message ? <div className="timeline-message">{event.message}</div> : null}
                  {hasObjectFields(event.details_json) ? <CodeBlock value={formatJSON(event.details_json, t)} /> : null}
                </div>
              </article>
            ))}
          </div>
        ) : (
          <EmptyState title={t("lineage.noExecutionEvents")} detail={t("lineage.noExecutionEventsDetail")} compact />
        )}
      </section>

      <section className="responses-audit-section">
        <div className="panel-head panel-head-compact">
          <div>
            <h2>{t("audit.modelExchanges")}</h2>
          </div>
          <InlineTag>{exchanges.length}</InlineTag>
        </div>
        {exchanges.length ? <ResponsesExchangeTable exchanges={exchanges} /> : <EmptyState title={t("lineage.noModelExchanges")} detail={t("lineage.noModelExchangesDetail")} compact />}
      </section>
    </div>
  );
}

function ResponsesEntryExchangeSummary({ exchange }) {
  const { t } = useI18n();
  return (
    <section className="responses-audit-section">
      <div className="panel-head panel-head-compact">
        <div>
          <h2>{t("audit.entryExchange")}</h2>
        </div>
        <InlineTag tone={exchange.error_text || Number(exchange.status_code || 0) >= 400 ? "danger" : "green"}>{exchange.status_code || (exchange.error_text ? t("lineage.error") : t("lineage.entry"))}</InlineTag>
      </div>
      <div className="detail-meta-strip responses-entry-exchange-summary">
        <DetailMetaPill label={t("lineage.kind")} value={exchangeLabel(exchange.exchange_kind || "entry", t)} />
        <DetailMetaPill label={t("lineage.role")} value={exchangeLabel(exchange.exchange_role || "-", t)} />
        <DetailMetaPill label={t("lineage.sequence")} value={formatSequence(exchange.sequence_index)} />
        <DetailMetaPill label={t("common.model")} value={exchange.model || "-"} />
        <DetailMetaPill label={t("lineage.provider")} value={exchange.provider || "-"} />
        <DetailMetaPill label={t("common.endpoint")} value={exchange.endpoint || "-"} mono />
        <DetailMetaPill label={t("lineage.cassette")} value={exchange.cassette_path || "-"} mono />
      </div>
      {exchange.error_text ? <pre className="timeline-message responses-audit-error">{exchange.error_text}</pre> : null}
    </section>
  );
}

function ResponsesExchangeTable({ exchanges }) {
  const { t } = useI18n();
  return (
    <div className="responses-exchange-table">
      <div className="responses-exchange-head">
        <span>{t("lineage.exchange")}</span>
        <span>{t("lineage.modelProvider")}</span>
        <span>{t("common.endpoint")}</span>
        <span>{t("lineage.status")}</span>
        <span>{t("common.trace")}</span>
        <span>{t("lineage.cassette")}</span>
        <span>{t("lineage.started")}</span>
      </div>
      {exchanges.map((exchange) => (
        <div className="responses-exchange-row" key={exchange.id || exchange.trace_id || `${exchange.exchange_role || "exchange"}-${exchange.sequence_index || 0}`}>
          <span>
            <span className="responses-exchange-primary">{exchangeLabel(exchange.exchange_role || exchange.exchange_kind || "model", t)}</span>
            <span className="responses-exchange-subline">{exchangeLabel(exchange.exchange_kind || "model", t)} / {t("lineage.seq")} {formatSequence(exchange.sequence_index)}</span>
          </span>
          <span>
            <span className="responses-exchange-primary">{exchange.model || "-"}</span>
            <span className="responses-exchange-subline">{exchange.provider || exchange.upstream_id || exchange.route_target || "-"}</span>
          </span>
          <span className="mono">{exchange.endpoint || "-"}</span>
          <span>
            <InlineTag tone={exchange.error_text || Number(exchange.status_code || 0) >= 400 ? "danger" : "green"}>{exchange.status_code || (exchange.error_text ? t("lineage.error") : "-")}</InlineTag>
          </span>
          <span className="mono">{exchange.trace_id ? <Link to={`/traces/${encodeURIComponent(exchange.trace_id)}`}>{exchange.trace_id}</Link> : exchange.id || "-"}</span>
          <span className="mono">{exchange.cassette_path || "-"}</span>
          <span>{formatDateTime(exchange.started_at)}</span>
          {exchange.error_text ? <span className="responses-exchange-error">{exchange.error_text}</span> : null}
        </div>
      ))}
    </div>
  );
}

function formatSequence(value) {
  return Number.isFinite(Number(value)) && Number(value) !== 0 ? String(value) : "0";
}

function exchangeLabel(value = "", t) {
  switch (String(value || "").trim()) {
    case "primary_model_call":
      return t("lineage.model");
    case "client_request":
      return t("lineage.request");
    case "upstream_model_call":
    case "model_call":
      return t("lineage.model");
    case "model":
      return t("lineage.model");
    case "entry":
      return t("lineage.request");
    case "-":
      return "-";
    default:
      return value || "-";
  }
}

function formatJSON(value, t) {
  if (!hasObjectFields(value)) {
    return t("lineage.noJSON");
  }
  return JSON.stringify(value, null, 2);
}

function hasObjectFields(value) {
  return Boolean(value && typeof value === "object" && Object.keys(value).length);
}
