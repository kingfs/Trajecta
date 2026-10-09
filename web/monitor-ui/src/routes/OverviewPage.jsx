import React, { useState } from "react";
import { Card } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Link, useSearchParams } from "react-router-dom";
import { BreakdownList } from "../components/monitor/BreakdownList";
import { MultiLineChart } from "../components/common/Charts";
import { InlineTag } from "../components/common/Badges";
import { StatCard } from "../components/common/Display";
import { EmptyState } from "../components/common/EmptyState";
import { PageHeader } from "../components/common/PageHeader";
import { WindowToggle } from "../components/common/Tabs";
import { SegmentedControl, SegmentedControlItem } from "../components/ui/segmented-control";
import { RequestList } from "../components/monitor/RequestList";
import { useJSON } from "../hooks/useJSON";
import { apiPaths, apiURL } from "../lib/api";
import { useI18n } from "../lib/i18n";
import {
  buildRoutingLink,
  buildTraceLink,
  formatCount,
  formatDateTime,
  formatDuration,
  formatEndpointTag,
  formatFailureReason,
  formatProviderTag,
  formatTokenCount,
  normalizeAnalyticsWindow,
  normalizeUpstreamWindow,
  setOrDeleteParam,
} from "../lib/monitor";

const REFRESH_MS = 60_000;

// The overview answers three questions in order: how much traffic and how did it
// go (the tiles), what does the shape of that traffic look like (trends and
// distribution), and what needs a human (the attention lists). Two blocks that
// used to live here did not belong to any of them: the provider card grid was a
// second, worse copy of the provider page, and derived-data health is a property
// of the parsing pipeline rather than of the last hour of traffic - it now has
// its own tab under 质量.
export function OverviewPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const [breakdownKind, setBreakdownKind] = useState("models");
  const { loading, data, error } = useJSON(apiURL(apiPaths.overview, { window: windowValue }), [windowValue], { refetchInterval: REFRESH_MS });
  const { data: eventSummary } = useJSON(apiURL(apiPaths.eventsSummary, { window: windowValue }), [windowValue], { refetchInterval: REFRESH_MS });

  const summary = data?.summary || {};
  const breakdown = data?.breakdown || {};
  const attention = data?.attention || {};
  const findingCount = (breakdown.finding_categories || []).reduce((sum, item) => sum + Number(item.count || 0), 0);

  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    setSearchParams(next);
  };

  const breakdownOptions = [
    { id: "models", label: t("nav.models"), items: breakdown.models || [], formatter: (item) => item.label || "unknown-model" },
    { id: "providers", label: t("nav.providers"), items: breakdown.providers || [], formatter: (item) => formatProviderTag(item.label) },
    { id: "endpoints", label: t("overview.endpoints"), items: breakdown.endpoints || [], formatter: (item) => formatEndpointTag(item.label) },
    { id: "upstreams", label: t("overview.upstreams"), items: breakdown.upstreams || [], formatter: (item) => item.label || "unknown-upstream" },
    { id: "routing_failures", label: t("overview.routingFailures"), items: breakdown.routing_failure_reasons || [], formatter: (item) => formatFailureReason(item.label) },
    { id: "finding_categories", label: t("overview.findingCategories"), items: breakdown.finding_categories || [], formatter: (item) => formatFailureReason(item.label) },
  ];
  const activeBreakdown = breakdownOptions.find((option) => option.id === breakdownKind) || breakdownOptions[0];

  return (
    <main className="shell shell-list">
      <PageHeader
        eyebrow={t("nav.group.observe")}
        title={t("overview.title")}
        meta={
          <>
            <WindowToggle value={windowValue} onChange={setWindow} label={t("overview.window")} />
            <span className="badge badge-live">{t("overview.refresh")}</span>
            <span className="badge">{data?.refreshed_at ? formatDateTime(data.refreshed_at) : "..."}</span>
          </>
        }
      />

      {error ? <EmptyState title={t("overview.loadError")} detail={error} tone="danger" /> : null}
      {loading && !data ? <EmptyState title={t("overview.loading")} detail={t("overview.loadingDetail")} /> : null}

      <section className="hero-grid overview-kpi-grid" aria-label={t("overview.title")}>
        <StatCard label={t("overview.requests")} value={formatCount(summary.request_count ?? 0)} detail={t("overview.activeSessions", { count: formatCount(summary.session_count ?? 0) })} />
        <StatCard label={t("common.successRate")} value={`${Number(summary.success_rate ?? 0).toFixed(1)}%`} detail={t("overview.successful", { count: formatCount(summary.success_request ?? 0) })} accent="accent-green" />
        <StatCard label={t("overview.failed")} value={formatCount(summary.failed_request ?? 0)} detail={t("overview.recentFailures", { count: attention.recent_failures?.length ?? 0 })} accent={(summary.failed_request ?? 0) > 0 ? "accent-red" : ""} />
        <StatCard label={t("overview.tokens")} value={formatTokenCount(summary.total_tokens ?? 0)} detail={t("overview.streamingTraces", { count: formatCount(summary.stream_count ?? 0) })} title={String(summary.total_tokens ?? 0)} />
        <StatCard label={t("common.avgTtft")} value={formatDuration(summary.avg_ttft_ms ?? 0)} detail={`p95 ${formatDuration(summary.p95_ttft_ms ?? 0)}`} />
        <StatCard label={t("overview.latency")} value={formatDuration(summary.avg_duration_ms ?? 0)} detail={`p95 ${formatDuration(summary.p95_duration_ms ?? 0)}`} />
      </section>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("overview.workspaceActivity")}</h2>
          </div>
          <div className="panel-head-actions">
            <span className="system-note">{t("overview.trend")}</span>
          </div>
        </div>
        <div className="overview-chart-grid">
          <section className="usage-chart-panel">
            <div className="breakdown-title">{t("overview.requestsFailures")}</div>
            <MultiLineChart
              items={(data?.timeline || []).map((item) => ({
                time: item.time,
                series: {
                  requests: { value: item.request_count },
                  failures: { value: item.failed_request },
                },
              }))}
              series={[
                { key: "requests", name: t("overview.requests") },
                { key: "failures", name: t("overview.failed") },
              ]}
              metric="value"
              height={220}
            />
          </section>
          <section className="usage-chart-panel">
            <div className="breakdown-title">{t("overview.tokens")}</div>
            <MultiLineChart
              items={(data?.timeline || []).map((item) => ({ time: item.time, value: item.total_tokens }))}
              series={[{ key: "value", name: t("overview.tokens") }]}
              metric="value"
              height={220}
            />
          </section>
          <section className="usage-chart-panel">
            <div className="breakdown-title">{t("overview.ttftLatency")}</div>
            <MultiLineChart
              items={(data?.timeline || []).map((item) => ({
                time: item.time,
                series: {
                  ttft: { value: Number(item.avg_ttft_ms || 0) / 1000 },
                  latency: { value: Number(item.avg_duration_ms || 0) / 1000 },
                },
              }))}
              series={[
                { key: "ttft", name: `${t("common.avgTtft")} s` },
                { key: "latency", name: `${t("overview.latency")} s` },
              ]}
              metric="value"
              height={220}
            />
          </section>
        </div>
      </Card>

      {/* Six separate breakdown lists used to be six full-height columns of the
          same shape; one list plus a dimension switch shows the same data
          without making the page scroll sideways. */}
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("overview.topBreakdowns")}</h2>
          </div>
          <div className="panel-head-actions">
            <SegmentedControl value={breakdownKind} onValueChange={setBreakdownKind} aria-label={t("overview.distribution")}>
              {breakdownOptions.map((option) => (
                <SegmentedControlItem key={option.id} value={option.id} active={breakdownKind === option.id}>
                  {option.label}
                </SegmentedControlItem>
              ))}
            </SegmentedControl>
          </div>
        </div>
        <BreakdownList
          title={activeBreakdown.label}
          items={activeBreakdown.items}
          formatter={activeBreakdown.formatter}
          linkFor={(item) => buildOverviewBreakdownLink(activeBreakdown.id, item.label, windowValue)}
        />
      </Card>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("overview.needsReview")}</h2>
          </div>
          <div className="panel-head-actions">
            <Button asChild variant="ghost" className="icon-text-button" to="/audit">
              <Link to="/audit">
                <span>{t("overview.audit")}</span>
                <span className="nav-item-badge">{formatCount(findingCount)}</span>
              </Link>
            </Button>
            <Button asChild variant="ghost" className="icon-text-button" to="/events">
              <Link to="/events">
                <span>{t("overview.systemEvents")}</span>
                <span className={`nav-item-badge${Number(eventSummary?.unread || 0) > 0 ? " nav-item-badge-alert" : ""}`}>{formatCount(eventSummary?.unread ?? 0)}</span>
              </Link>
            </Button>
            <Button asChild variant="ghost" to={buildRoutingLink(normalizeUpstreamWindow(windowValue))}><Link to={buildRoutingLink(normalizeUpstreamWindow(windowValue))}>{t("overview.routing")}</Link></Button>
          </div>
        </div>
        <div className="overview-attention-grid">
          <AttentionPanel title={t("overview.recentFailuresTitle")} emptyTitle={t("overview.noRecentFailures")}>
            {(attention.recent_failures || []).length ? <RequestList items={attention.recent_failures || []} fromView="overview" focusFailures /> : null}
          </AttentionPanel>
          <AttentionPanel title={t("overview.slowTraces")} emptyTitle={t("overview.noSlowTraces")}>
            {(attention.slow_traces || []).length ? <RequestList items={attention.slow_traces || []} fromView="overview" /> : null}
          </AttentionPanel>
          <AttentionPanel title={t("overview.highRiskFindings")} emptyTitle={t("overview.noHighRiskFindings")}>
            {(attention.high_risk_findings || []).length ? <FindingQueue items={attention.high_risk_findings || []} /> : null}
          </AttentionPanel>
          <AttentionPanel title={t("overview.routingFailures")} emptyTitle={t("overview.noRoutingFailures")}>
            {(attention.routing_failures || []).length ? <RoutingFailureQueue items={attention.routing_failures || []} /> : null}
          </AttentionPanel>
        </div>
      </Card>
    </main>
  );
}

function AttentionPanel({ title, emptyTitle, children }) {
  const { t } = useI18n();
  return (
    <section className="overview-attention-panel">
      <div className="breakdown-title">{title}</div>
      {children || <EmptyState title={emptyTitle} detail={t("overview.noAttentionDetail")} compact />}
    </section>
  );
}

function FindingQueue({ items }) {
  const { t } = useI18n();
  return (
    <div className="overview-queue">
      {items.map((item) => (
        <Link className="overview-queue-row" key={item.id} to={buildTraceLink(item.trace_id, "overview", "", "audit", item.node_id || item.evidence_path || "finding")}>
          <div>
            <strong>{item.title || item.category || t("overview.finding")}</strong>
            <span>{item.evidence_path || item.trace_id}</span>
          </div>
          <div className="trace-tag-group">
            <InlineTag tone={item.severity === "critical" ? "danger" : "gold"}>{item.severity}</InlineTag>
            <InlineTag>{formatFailureReason(item.category)}</InlineTag>
          </div>
        </Link>
      ))}
    </div>
  );
}

function RoutingFailureQueue({ items }) {
  return (
    <div className="overview-queue">
      {items.map((item) => (
        <Link className="overview-queue-row" key={`${item.trace_id}-${item.recorded_at}`} to={buildTraceLink(item.trace_id, "overview", "", "", "failure")}>
          <div>
            <strong>{item.model || "unknown-model"}</strong>
            <span>{formatDateTime(item.recorded_at)}</span>
          </div>
          <div className="trace-tag-group">
            <InlineTag tone="danger">{item.status_code}</InlineTag>
            <InlineTag tone="accent">{formatEndpointTag(item.endpoint)}</InlineTag>
            <InlineTag>{formatFailureReason(item.reason)}</InlineTag>
          </div>
        </Link>
      ))}
    </div>
  );
}

function buildOverviewBreakdownLink(kind, value, windowValue) {
  const label = String(value || "").trim();
  if (!label) {
    return "";
  }
  switch (kind) {
    case "models":
      return `/models/${encodeURIComponent(label)}${windowValue && windowValue !== "today" ? `?window=${encodeURIComponent(windowValue)}` : ""}`;
    case "providers":
      return `/traces?provider=${encodeURIComponent(label)}`;
    case "endpoints":
      return `/traces?q=${encodeURIComponent(label)}`;
    case "upstreams":
      return buildRoutingLink(normalizeUpstreamWindow(windowValue), label);
    case "routing_failures":
      return `/routing?status=error${windowValue && windowValue !== "today" ? `&window=${encodeURIComponent(windowValue)}` : ""}`;
    case "finding_categories":
      return `/audit?category=${encodeURIComponent(label)}`;
    default:
      return "";
  }
}
