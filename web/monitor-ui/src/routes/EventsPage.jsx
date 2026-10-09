import React, { useMemo, useState } from "react";
import { Card } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Button } from "../components/ui/button";
import { Link, useSearchParams } from "react-router-dom";
import { InlineTag } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { PageHeader } from "../components/common/PageHeader";
import { WindowToggle } from "../components/common/Tabs";
import { StatCard } from "../components/common/Display";
import { useJSON } from "../hooks/useJSON";
import { useRefresh } from "../hooks/useRefresh";
import { apiPaths, apiURL, postJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { buildTraceLink, formatDateTime, formatFailureReason, setOrDeleteParam } from "../lib/monitor";

const DEFAULT_EVENT_WINDOW = "all";
const STATUS_OPTIONS = ["unread", "read", "resolved", "ignored", "all"];
const SEVERITY_OPTIONS = ["all", "critical", "error", "warning", "info"];
const SOURCE_OPTIONS = ["all", "parser", "analyzer", "router", "upstream", "proxy", "recorder", "monitor", "store", "auth", "mcp"];

export function EventsPage() {
  const refresh = useRefresh();
  const { language, t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const [selectedID, setSelectedID] = useState("");
  const params = useMemo(() => eventQueryParams(searchParams), [searchParams]);
  const { loading, data, error } = useJSON(apiURL(apiPaths.events, params), [params.toString()]);
  const summaryWindow = params.get("window") || DEFAULT_EVENT_WINDOW;
  const { data: summary } = useJSON(apiURL(apiPaths.eventsSummary, { window: summaryWindow }), [summaryWindow]);
  const items = data?.items || [];
  const selected = items.find((item) => item.id === selectedID) || items[0] || null;

  const setFilter = (key, value) => {
    const next = new URLSearchParams(searchParams);
    const shouldDelete = key === "window" ? value === DEFAULT_EVENT_WINDOW : value === "all";
    setOrDeleteParam(next, key, shouldDelete ? "" : value);
    if (key !== "page") {
      next.delete("page");
    }
    setSearchParams(next);
  };

  // Success stays quiet here on purpose: the row and the badge change in front of
  // the reader, so a toast per click would be noise. Failure used to be quieter
  // still - the old `finally` only cleared the busy flag, so an action that the
  // server rejected did nothing visible at all.
  const announce = () => {
    refresh();
  };

  const mutateEvent = useWriteMutation({
    mutationFn: ({ path }) => postJSON(path, {}),
    error: "common.actionFailed",
    onSuccess: announce,
  });

  const markAllRead = useWriteMutation({
    mutationFn: () => postJSON(apiURL(apiPaths.eventsReadAll, params), {}),
    error: "common.actionFailed",
    onSuccess: announce,
  });

  const mutateEventAction = (eventID, action) => {
    const path = {
      read: apiPaths.eventRead(eventID),
      resolve: apiPaths.eventResolve(eventID),
      ignore: apiPaths.eventIgnore(eventID),
    }[action];
    if (!path) {
      return;
    }
    mutateEvent.mutate({ key: `${eventID}:${action}`, path });
  };

  const busyID = mutateEvent.isPending ? mutateEvent.variables?.key || "" : markAllRead.isPending ? "read-all" : "";

  return (
    <div className="shell shell-list">
      <PageHeader
        title={t("events.title")}
        actions={
          <>
            <Button variant="ghost" type="button" onClick={() => markAllRead.mutate()} disabled={busyID === "read-all"}>
              {t("events.markAllRead")}
            </Button>
            <WindowToggle value={currentFilter(searchParams, "window", DEFAULT_EVENT_WINDOW)} onChange={(next) => setFilter("window", next)} label={t("events.window")} />
          </>
        }
      />

      {error ? <EmptyState title={t("events.loadError")} detail={error} tone="danger" /> : null}
      {loading && !data ? <EmptyState title={t("events.loading")} detail={t("events.loadingDetail")} /> : null}

      <section className="hero-grid overview-kpi-grid">
        <StatCard label={t("events.unread")} value={summary?.unread ?? 0} detail={t("events.totalEvents", { count: summary?.total ?? 0 })} accent={(summary?.unread ?? 0) ? "accent-red" : "accent-green"} />
        <StatCard label={t("events.critical")} value={summary?.critical ?? 0} detail={t("events.criticalDetail")} accent={(summary?.critical ?? 0) ? "accent-red" : ""} />
        <StatCard label={t("events.error")} value={summary?.error ?? 0} detail={t("events.errorDetail")} accent={(summary?.error ?? 0) ? "accent-red" : ""} />
        <StatCard label={t("events.warning")} value={summary?.warning ?? 0} detail={t("events.warningDetail")} accent={(summary?.warning ?? 0) ? "accent-gold" : ""} />
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("events.runtimeExceptions")}</h2>
          </div>
          <span className="badge">{t("events.matching", { count: data?.total ?? 0 })}</span>
        </div>
        <div className="filter-bar event-filter-bar">
          <select className="filter-input" value={currentFilter(searchParams, "status", "unread")} onChange={(event) => setFilter("status", event.target.value)} aria-label={t("events.status")}>
            {STATUS_OPTIONS.map((option) => <option key={option} value={option}>{formatEventOption(option, t, language)}</option>)}
          </select>
          <select className="filter-input" value={currentFilter(searchParams, "severity", "all")} onChange={(event) => setFilter("severity", event.target.value)} aria-label={t("events.severity")}>
            {SEVERITY_OPTIONS.map((option) => <option key={option} value={option}>{formatEventOption(option, t, language)}</option>)}
          </select>
          <select className="filter-input" value={currentFilter(searchParams, "source", "all")} onChange={(event) => setFilter("source", event.target.value)} aria-label={t("events.source")}>
            {SOURCE_OPTIONS.map((option) => <option key={option} value={option}>{formatEventOption(option, t, language)}</option>)}
          </select>
          <Input className="min-w-[260px]" type="search" value={searchParams.get("q") || ""} onChange={(event) => setFilter("q", event.target.value)} placeholder={t("events.search")} />
        </div>
      </Card>

      <div className="event-workspace">
        <Card as="section" className="event-list-panel">
          <div className="event-list">
            {items.length ? items.map((item) => (
              <button key={item.id} className={selected?.id === item.id ? "event-row event-row-active" : "event-row"} type="button" onClick={() => setSelectedID(item.id)}>
                <span className={`event-severity event-severity-${item.severity || "info"}`} />
                <span className="event-row-main">
                  <strong>{item.title || item.fingerprint}</strong>
                  <span>{item.message || item.fingerprint}</span>
                </span>
                <span className="event-row-meta">
                  <InlineTag tone={severityTone(item.severity)}>{item.severity}</InlineTag>
                  <InlineTag>{item.status}</InlineTag>
                  <small>{formatDateTime(item.last_seen_at)}</small>
                </span>
              </button>
            )) : <EmptyState title={t("events.noMatch")} detail={t("events.noMatchDetail")} compact />}
          </div>
        </Card>

        <Card as="section" className="event-detail-panel">
          {selected ? (
            <EventDetail event={selected} busyID={busyID} onAction={mutateEventAction} />
          ) : (
            <EmptyState title={t("events.noSelected")} detail={t("events.noSelectedDetail")} />
          )}
        </Card>
      </div>
    </div>
  );
}

function EventDetail({ event, busyID, onAction }) {
  const { t } = useI18n();
  return (
    <div className="event-detail">
      <div className="panel-head event-detail-head">
        <div>
          <h2>{event.title || event.fingerprint}</h2>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone={severityTone(event.severity)}>{event.severity}</InlineTag>
          <InlineTag>{event.status}</InlineTag>
        </div>
      </div>
      <p className="event-message">{event.message || event.fingerprint}</p>
      <div className="detail-meta-strip event-meta-strip">
        <Meta label="occurrences" value={event.occurrence_count ?? 1} />
        <Meta label="first seen" value={formatDateTime(event.first_seen_at)} />
        <Meta label="last seen" value={formatDateTime(event.last_seen_at)} />
        <Meta label="fingerprint" value={event.fingerprint} mono />
      </div>
      <div className="trace-tag-group event-links">
        {event.trace_id ? <Button asChild variant="ghost" to={buildTraceLink(event.trace_id, "events", event.session_id || "", "protocol", "observation")}><Link to={buildTraceLink(event.trace_id, "events", event.session_id || "", "protocol", "observation")}>{t("events.trace")}</Link></Button> : null}
        {event.session_id ? <Button asChild variant="ghost" to={`/sessions/${encodeURIComponent(event.session_id)}`}><Link to={`/sessions/${encodeURIComponent(event.session_id)}`}>{t("events.session")}</Link></Button> : null}
        {event.upstream_id ? <Button asChild variant="ghost" to={`/upstreams/${encodeURIComponent(event.upstream_id)}`}><Link to={`/upstreams/${encodeURIComponent(event.upstream_id)}`}>{t("events.upstream")}</Link></Button> : null}
      </div>
      <div className="event-actions">
        <Button variant="ghost" type="button" onClick={() => onAction(event.id, "read")} disabled={busyID === `${event.id}:read` || event.status === "read"}>{t("events.markRead")}</Button>
        <Button variant="ghost" type="button" onClick={() => onAction(event.id, "resolve")} disabled={busyID === `${event.id}:resolve` || event.status === "resolved"}>{t("events.resolve")}</Button>
        <Button variant="ghost" type="button" onClick={() => onAction(event.id, "ignore")} disabled={busyID === `${event.id}:ignore` || event.status === "ignored"}>{t("events.ignore")}</Button>
      </div>
      <pre className="code-block event-details-json">{formatJSON(event.details_json)}</pre>
    </div>
  );
}

function Meta({ label, value, mono = false }) {
  return <span className="detail-meta-pill"><span className="detail-meta-label">{label}</span><strong className={mono ? "mono" : ""}>{value || "-"}</strong></span>;
}

function formatEventOption(option, t, language) {
  if (language === "en") {
    return option;
  }
  const map = {
    all: "全部",
    unread: "未读",
    read: "已读",
    resolved: "已解决",
    ignored: "已忽略",
    critical: t("events.critical"),
    error: t("events.error"),
    warning: t("events.warning"),
    info: "信息",
  };
  return map[option] || option;
}

function eventQueryParams(searchParams) {
  const params = new URLSearchParams();
  params.set("window", currentFilter(searchParams, "window", DEFAULT_EVENT_WINDOW));
  params.set("status", currentFilter(searchParams, "status", "unread"));
  for (const key of ["severity", "source", "category", "q", "page"]) {
    const value = searchParams.get(key);
    if (value && value !== "all") {
      params.set(key, value);
    }
  }
  params.set("page_size", "50");
  return params;
}

function currentFilter(searchParams, key, fallback) {
  return searchParams.get(key) || fallback;
}

function severityTone(severity = "") {
  switch (severity) {
    case "critical":
    case "error":
      return "danger";
    case "warning":
      return "gold";
    default:
      return "default";
  }
}

function formatJSON(value) {
  if (!value) {
    return "{}";
  }
  try {
    return JSON.stringify(typeof value === "string" ? JSON.parse(value) : value, null, 2);
  } catch {
    return String(value);
  }
}
