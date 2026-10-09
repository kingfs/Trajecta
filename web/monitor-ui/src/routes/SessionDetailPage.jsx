import React, { useState } from "react";
import { Card } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { toast } from "sonner";
import { Link, useParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { SegmentedControl, SegmentedControlItem } from "../components/ui/segmented-control";
import { DetailMetaPill, HomeIcon, InlineTag, TokenBadge, ViewIcon } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { BreakdownList } from "../components/monitor/BreakdownList";
import { RequestList } from "../components/monitor/RequestList";
import { SessionTrajectory } from "../components/monitor/SessionTrajectory";
import { useJSON } from "../hooks/useJSON";
import { apiPaths, downloadBlob, postJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import {
  buildFailureContexts,
  buildFailureDelta,
  buildFailureDetail,
  buildFailureSummary,
  buildTraceLink,
  formatDateTime,
  formatDuration,
  formatEndpointTag,
  formatFailureReason,
  formatProviderTag,
  formatSignedMetric,
  formatTokenCount,
  formatTokenRate,
} from "../lib/monitor";

export function SessionDetailPage() {
  const { sessionID = "" } = useParams();
  const { t } = useI18n();
  const [traceFilter, setTraceFilter] = useState("all");
  const [tab, setTab] = useState("timeline");
  const detail = useJSON(apiPaths.session(sessionID), [sessionID]);
  const trajectory = useJSON(apiPaths.sessionTrajectory(sessionID), [sessionID]);
  const summary = detail.data?.summary;
  const breakdown = detail.data?.breakdown;
  const timeline = detail.data?.timeline ?? [];
  const traces = detail.data?.traces ?? [];
  const performance = detail.data?.performance;
  const analysis = detail.data?.analysis ?? [];
  const visibleTraces = traceFilter === "failed" ? traces.filter((trace) => trace.status_code < 200 || trace.status_code >= 300) : traces;
  const failureContexts = buildFailureContexts(timeline);

  const downloadSessionExport = async (path, extension) => {
    const blob = await downloadBlob(path);
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `session-${sessionID.replace(/[^a-zA-Z0-9_-]/g, "_")}${extension}`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    return blob;
  };

  // An export has three outcomes rather than two - the file can arrive complete,
  // with warnings, or truncated - so it picks its own toast from the result.
  const exportTrajectory = useWriteMutation({
    mutationFn: async () => {
      const blob = await downloadSessionExport(apiPaths.sessionTrajectory(sessionID), ".atif.jsonl");
      return JSON.parse(await blob.text());
    },
    error: "sessionDetail.exportError",
    onSuccess: (trajectory) => {
      const warnings = trajectory.extra?.warnings?.length || 0;
      if (trajectory.extra?.truncated) {
        toast.warning(t("sessionDetail.exportTruncated", { included: trajectory.extra?.included_traces ?? 0, total: trajectory.extra?.trace_count ?? 0 }));
        return;
      }
      if (warnings) {
        toast.error(t("sessionDetail.exportWarnings", { count: warnings }));
        return;
      }
      toast.success(t("sessionDetail.exportDone"));
    },
  });

  const exportFullTrajectory = useWriteMutation({
    mutationFn: () => downloadSessionExport(apiPaths.sessionTrajectory(sessionID, { full: true, stream: true }), ".atif.ndjson"),
    success: "sessionDetail.exportFullDone",
    error: "sessionDetail.exportError",
  });

  const reanalyzeSession = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.sessionReanalyze(sessionID), { mode: "async", reparse: true, scan: true }),
    success: (response) => t("sessionDetail.refreshJobNotice", { id: response?.job?.id || "-", status: response?.job?.status || "queued" }),
    error: "sessionDetail.requestFailed",
    onSuccess: () => setTab("analysis"),
  });

  return (
    <div className="shell shell-detail">
      <header className="topbar detail-topbar">
        <div className="detail-title-block">
          <div className="detail-heading-row">
            <h1>{summary?.last_model || t("sessionDetail.sessionFallback")}</h1>
            <div className="trace-tag-group detail-tag-group">
              <InlineTag tone="accent">{summary?.session_source || "session"}</InlineTag>
              {(summary?.providers || []).map((provider) => (
                <InlineTag key={provider}>{formatProviderTag(provider)}</InlineTag>
              ))}
            </div>
          </div>
          <div className="detail-meta-strip">
            <DetailMetaPill label={t("sessionDetail.metaSession")} value={summary?.session_id || sessionID} mono />
            <DetailMetaPill label={t("sessionDetail.firstSeen")} value={formatDateTime(summary?.first_seen)} />
            <DetailMetaPill label={t("sessionDetail.lastSeen")} value={formatDateTime(summary?.last_seen)} />
            <DetailMetaPill label={t("common.requests")} value={summary?.request_count ?? 0} />
            <DetailMetaPill label={t("common.success")} value={`${Number(summary?.success_rate ?? 0).toFixed(1)}%`} />
          </div>
        </div>
        <div className="topbar-meta detail-toolbar">
          <div className="detail-toolbar-actions">
            <Button asChild variant="default" size="icon">
              <Link to="/traces?tab=sessions" title={t("sessionDetail.backToSessions")} aria-label={t("sessionDetail.backToSessions")}>
                <HomeIcon />
              </Link>
            </Button>
            <Button variant="ghost" type="button" disabled={exportTrajectory.isPending || exportFullTrajectory.isPending || !detail.data} onClick={() => exportTrajectory.mutate()}>
              {exportTrajectory.isPending ? t("sessionDetail.exporting") : t("sessionDetail.exportTrajectory")}
            </Button>
            <Button variant="ghost" type="button" disabled={exportTrajectory.isPending || exportFullTrajectory.isPending || !detail.data} onClick={() => exportFullTrajectory.mutate()}>
              {exportFullTrajectory.isPending ? t("sessionDetail.exporting") : t("sessionDetail.exportTrajectoryFull")}
            </Button>
            <Button variant="primary" type="button" disabled={reanalyzeSession.isPending} onClick={() => reanalyzeSession.mutate()}>
              {reanalyzeSession.isPending ? t("analysis.queueing") : t("sessionDetail.refreshAnalysis")}
            </Button>
          </div>
          <div className="detail-toolbar-tokens">
            <TokenBadge label={t("metric.ttft")} value={summary?.avg_ttft ?? 0} icon="duration" format="duration" />
            <TokenBadge label={t("metric.totalTokens")} value={summary?.total_tokens ?? 0} icon="total" accent="token-badge-strong" />
            <TokenBadge label={t("common.failed")} value={summary?.failed_request ?? 0} icon="failed" />
          </div>
        </div>
      </header>

      {detail.error ? <EmptyState title={t("sessionDetail.loadError")} detail={detail.error} tone="danger" /> : null}
      {detail.loading && !detail.data ? <EmptyState title={t("sessionDetail.loading")} detail={t("sessionDetail.loadingDetail")} /> : null}

      {detail.data ? (
        <>
          <SessionTrajectory trajectory={trajectory.data} loading={trajectory.loading} error={trajectory.error} />
          <SessionTabs tab={tab} setTab={setTab} analysisCount={analysis.length} failedCount={breakdown?.failed_traces ?? 0} />
          <div className="detail-grid detail-grid-compact">
            <Card as="section">
              <div className="panel-head">
                <div>
                  <h2>{t("sessionDetail.sessionHealth")}</h2>
                </div>
              </div>
              <div className="hero-grid hero-grid-compact">
                <StatCard label={t("common.failed")} value={breakdown?.failed_traces ?? 0} accent={(breakdown?.failed_traces ?? 0) > 0 ? "accent-red" : ""} />
                <StatCard label={t("common.success")} value={summary?.success_request ?? 0} />
                <StatCard label={t("sessionDetail.streams")} value={summary?.stream_count ?? 0} />
                <StatCard label={t("sessionDetail.duration")} value={formatDuration(summary?.total_duration_ms ?? 0)} detail={`${formatDuration(summary?.total_duration_ms ?? 0)} total`} title={`${summary?.total_duration_ms ?? 0} ms`} />
              </div>
            </Card>
            <Card as="section">
              <div className="panel-head">
                <div>
                  <h2>{t("sessionDetail.modelsAndEndpoints")}</h2>
                </div>
              </div>
              <div className="session-breakdown-grid">
                <BreakdownList title={t("sessionDetail.models")} items={breakdown?.models || []} formatter={(item) => item.label} />
                <BreakdownList title={t("sessionDetail.endpoints")} items={breakdown?.endpoints || []} formatter={(item) => formatEndpointTag(item.label)} />
                <BreakdownList title={t("sessionDetail.failureReasons")} items={breakdown?.failure_reasons || []} formatter={(item) => formatFailureReason(item.label)} />
              </div>
            </Card>
          </div>
        </>
      ) : null}

      {detail.data && tab === "timeline" && timeline.length ? (
        <Card as="section" className="timeline-panel">
          <div className="panel-head">
            <div>
              <h2>{t("sessionDetail.requestSequence")}</h2>
            </div>
          </div>
          <div className="timeline-list">
            {timeline.map((item) => (
              <article key={item.trace_id} className="timeline-item">
                <div className="timeline-rail">
                  <span className={item.status_code >= 200 && item.status_code < 300 ? "timeline-dot" : "timeline-dot timeline-dot-danger"} />
                </div>
                <div className="timeline-card">
                  <div className="timeline-head">
                    <div>
                      <strong>{item.model || t("sessionDetail.unknownModel")}</strong>
                      <span>{formatDateTime(item.time)}</span>
                    </div>
                    <span className="timeline-badge">{item.is_stream ? "stream" : "request"}</span>
                  </div>
                  <div className="trace-tag-group">
                    <InlineTag tone="accent">{formatEndpointTag(item.endpoint)}</InlineTag>
                    <InlineTag>{formatProviderTag(item.provider)}</InlineTag>
                    <InlineTag tone={item.status_code >= 200 && item.status_code < 300 ? "green" : "danger"}>{item.status_code}</InlineTag>
                  </div>
                  <div className="session-timeline-meta">
                    <span>duration {formatDuration(item.duration_ms)}</span>
                    <span>ttft {formatDuration(item.ttft_ms)}</span>
                    <span>tokens {formatTokenCount(item.total_tokens)}</span>
                    <span>rate {formatTokenRate(item.total_tokens, item.duration_ms)}</span>
                  </div>
                  {item.error ? <div className="timeline-message">{item.error}</div> : null}
                  <div className="action-group action-group-start">
                    <Button asChild 
                      variant="ghost"
                      to={buildTraceLink(item.trace_id, "", summary?.session_id || sessionID, "raw", item.status_code >= 200 && item.status_code < 300 ? "timeline" : "timeline_error")}
                    >
                      <Link
                     
                      to={buildTraceLink(item.trace_id, "", summary?.session_id || sessionID, "raw", item.status_code >= 200 && item.status_code < 300 ? "timeline" : "timeline_error")}
                    >
                        {t("requests.timeline")}
                      </Link>
                    </Button>
                    <Button asChild variant="ghost" to={buildTraceLink(item.trace_id, "", summary?.session_id || sessionID, "raw", item.status_code >= 200 && item.status_code < 300 ? "" : "response")}>
                      <Link to={buildTraceLink(item.trace_id, "", summary?.session_id || sessionID, "raw", item.status_code >= 200 && item.status_code < 300 ? "" : "response")}>
                        {t("requests.raw")}
                      </Link>
                    </Button>
                    <Button asChild variant="default" size="icon">
                      <Link to={buildTraceLink(item.trace_id, "", summary?.session_id || sessionID, "", item.status_code >= 200 && item.status_code < 300 ? "" : "failure")} title={t("requests.viewTrace")} aria-label={t("requests.viewTrace")}>
                        <ViewIcon />
                      </Link>
                    </Button>
                  </div>
                </div>
              </article>
            ))}
          </div>
        </Card>
      ) : detail.data && tab === "timeline" ? (
        <EmptyState title={t("sessionDetail.noTimeline")} detail={t("sessionDetail.noTimelineDetail")} />
      ) : null}

      {detail.data && tab === "timeline" && failureContexts.length ? (
        <Card as="section">
          <div className="panel-head">
            <div>
              <h2>{t("sessionDetail.requestsAroundFailure")}</h2>
            </div>
          </div>
          <div className="failure-context-list">
            {failureContexts.map((context) => (
              <article key={context.current.trace_id} className="failure-context-card">
                <div className="failure-context-head">
                  <strong>{context.current.model || t("sessionDetail.unknownModel")}</strong>
                  <span>{formatDateTime(context.current.time)}</span>
                </div>
                <p className="failure-context-summary">{buildFailureSummary(context)}</p>
                <div className="failure-context-strip">
                  {context.previous ? <FailureContextNode label={t("sessionDetail.before")} item={context.previous} tone="default" sessionID={summary?.session_id || sessionID} /> : null}
                  <FailureContextNode
                    label={t("common.failed")}
                    item={context.current}
                    tone="danger"
                    sessionID={summary?.session_id || sessionID}
                    delta={buildFailureDelta(context.previous, context.current)}
                    detail={context.current.error || buildFailureDetail(context.current)}
                  />
                  {context.next ? <FailureContextNode label={t("sessionDetail.after")} item={context.next} tone="accent" sessionID={summary?.session_id || sessionID} /> : null}
                </div>
              </article>
            ))}
          </div>
        </Card>
      ) : detail.data && tab === "timeline" ? (
        <EmptyState title={t("sessionDetail.noFailureContext")} detail={t("sessionDetail.noFailureContextDetail")} />
      ) : null}

      {detail.data && tab === "traces" ? (
        <Card as="section">
          <div className="panel-head">
            <div>
              <h2>{traceFilter === "failed" ? t("sessionDetail.failedRequestList") : t("sessionDetail.groupedRequestList")}</h2>
            </div>
            <div className="panel-head-actions">
              <SegmentedControl type="single" value={traceFilter} onValueChange={setTraceFilter} aria-label={t("sessionDetail.traceFilterLabel")}>
                <SegmentedControlItem value="all" active={traceFilter === "all"}>
                  {t("sessionDetail.all")}
                </SegmentedControlItem>
                <SegmentedControlItem value="failed" active={traceFilter === "failed"}>
                  {t("sessionDetail.failedOnly")}
                </SegmentedControlItem>
              </SegmentedControl>
              <span className="session-filter-count">
                {t("sessionDetail.traceCount", { visible: visibleTraces.length, total: traces.length })}
              </span>
            </div>
          </div>
          {traceFilter === "failed" && visibleTraces.length === 0 ? (
            <EmptyState title={t("sessionDetail.noFailedTraces")} detail={t("sessionDetail.noFailedTracesDetail")} />
          ) : (
            <RequestList items={visibleTraces} fromSessionID={summary?.session_id || sessionID} focusFailures groupSessionFailures />
          )}
        </Card>
      ) : null}

      {detail.data && tab === "audit" ? <SessionAuditPanel failedCount={breakdown?.failed_traces ?? 0} traces={traces} sessionID={summary?.session_id || sessionID} /> : null}
      {detail.data && tab === "performance" ? <SessionPerformancePanel performance={performance} /> : null}
      {detail.data && tab === "analysis" ? <SessionAnalysisPanel analysis={analysis} /> : null}
    </div>
  );
}

function SessionTabs({ tab, setTab, analysisCount = 0, failedCount = 0 }) {
  const { t } = useI18n();
  const tabs = [
    { id: "timeline", label: t("requests.timeline"), detail: t("sessionDetail.requestSequence") },
    { id: "traces", label: t("nav.traces"), detail: t("sessionDetail.httpExchanges") },
    { id: "audit", label: t("nav.audit"), detail: t("sessionDetail.failedCount", { count: failedCount }) },
    { id: "performance", label: t("sessionDetail.tabPerformance"), detail: t("sessionDetail.latencyAndTokens") },
    { id: "analysis", label: t("nav.analysis"), detail: t(analysisCount === 1 ? "sessionDetail.runCountOne" : "sessionDetail.runCount", { count: analysisCount }) },
  ];
  return (
    <section className="session-tab-strip" aria-label={t("sessionDetail.sessionViews")}>
      {tabs.map((item) => (
        <button key={item.id} className={tab === item.id ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTab(item.id)}>
          <strong>{item.label}</strong>
          <span>{item.detail}</span>
        </button>
      ))}
    </section>
  );
}

function SessionAuditPanel({ failedCount, traces, sessionID }) {
  const { t } = useI18n();
  const failed = traces.filter((trace) => trace.status_code < 200 || trace.status_code >= 300);
  return (
    <Card as="section" className="audit-panel">
      <div className="panel-head">
        <div>
          <h2>{t("sessionDetail.riskEntryPoints")}</h2>
        </div>
        <InlineTag tone={failedCount ? "danger" : "green"}>{t("sessionDetail.failedCount", { count: failedCount })}</InlineTag>
      </div>
      {failed.length ? (
        <div className="finding-list">
          {failed.map((trace) => (
            <article key={trace.id} className="finding-card">
              <div className="finding-card-head">
                <div>
                  <strong>{trace.model || t("sessionDetail.unknownModel")}</strong>
                  <span>{formatDateTime(trace.time)}</span>
                </div>
                <div className="trace-tag-group">
                  <InlineTag tone="danger">{trace.status_code}</InlineTag>
                  <InlineTag>{formatEndpointTag(trace.endpoint)}</InlineTag>
                </div>
              </div>
              <div className="action-group action-group-start">
                <Button asChild variant="ghost" to={buildTraceLink(trace.id, "", sessionID, "audit", "failure")}><Link to={buildTraceLink(trace.id, "", sessionID, "audit", "failure")}>{t("nav.audit")}</Link></Button>
                <Button asChild variant="ghost" to={buildTraceLink(trace.id, "", sessionID, "raw", "response")}><Link to={buildTraceLink(trace.id, "", sessionID, "raw", "response")}>{t("requests.raw")}</Link></Button>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <EmptyState title={t("sessionDetail.noFailedTraces")} detail={t("sessionDetail.noAuditFailuresDetail")} compact />
      )}
    </Card>
  );
}

function SessionPerformancePanel({ performance }) {
  const { t } = useI18n();
  if (!performance) {
    return <EmptyState title={t("sessionDetail.noPerformanceData")} detail={t("sessionDetail.noPerformanceDataDetail")} />;
  }
  return (
    <Card as="section" className="performance-panel">
      <div className="panel-head">
        <div>
          <h2>{t("sessionDetail.latencyAndTokens")}</h2>
        </div>
      </div>
      <section className="hero-grid">
        <StatCard label={t("common.requests")} value={performance.request_count || 0} />
        <StatCard label={t("common.success")} value={`${Number(performance.success_rate || 0).toFixed(1)}%`} accent="accent-green" />
        <StatCard label={t("sessionDetail.duration")} value={formatDuration(performance.duration_ms || 0, { precise: true })} />
        <StatCard label="TTFT" value={formatDuration(performance.ttft_ms || 0, { precise: true })} />
        <StatCard label={t("sessionDetail.tokensPerSec")} value={Number(performance.tokens_per_sec || 0).toFixed(2)} accent="accent-gold" />
        <StatCard label={t("sessionDetail.cache")} value={`${Number(performance.cache_ratio || 0).toFixed(1)}%`} />
      </section>
      <div className="detail-meta-strip">
        <DetailMetaPill label={t("sessionDetail.totalTokens")} value={formatTokenCount(performance.total_tokens || 0)} />
        <DetailMetaPill label={t("sessionDetail.input")} value={formatTokenCount(performance.prompt_tokens || 0)} />
        <DetailMetaPill label={t("sessionDetail.output")} value={formatTokenCount(performance.completion_tokens || 0)} />
        <DetailMetaPill label={t("sessionDetail.cached")} value={formatTokenCount(performance.cached_tokens || 0)} />
      </div>
    </Card>
  );
}

function SessionAnalysisPanel({ analysis }) {
  const { t } = useI18n();
  return (
    <Card as="section" className="analysis-panel">
      <div className="panel-head">
        <div>
          <h2>{t("sessionDetail.analysisRuns")}</h2>
        </div>
      </div>
      {analysis.length ? (
        <div className="finding-list">
          {analysis.map((run) => (
            <article key={run.id} className="finding-card">
              <div className="finding-card-head">
                <div>
                  <strong>{run.kind}</strong>
                  <span>{run.analyzer} {run.analyzer_version}</span>
                </div>
                <div className="trace-tag-group">
                  <InlineTag tone={run.status === "completed" ? "green" : "gold"}>{run.status}</InlineTag>
                </div>
              </div>
              <div className="detail-meta-strip">
                <DetailMetaPill label={t("sessionDetail.input")} value={run.input_ref || "-"} mono />
                <DetailMetaPill label={t("common.created")} value={formatDateTime(run.created_at)} />
              </div>
              <pre className="code-block">{JSON.stringify(run.output || {}, null, 2)}</pre>
            </article>
          ))}
        </div>
      ) : (
        <EmptyState title={t("analysis.noRuns")} detail={t("sessionDetail.noAnalysisRunsDetail")} compact />
      )}
    </Card>
  );
}

function FailureContextNode({ label, item, tone = "default", sessionID = "", delta = null, detail = "" }) {
  const { t } = useI18n();
  const focus = tone === "danger" ? "failure" : "";
  const traceLink = buildTraceLink(item.trace_id, "", sessionID, "", focus);
  const timelineLink = buildTraceLink(item.trace_id, "", sessionID, "raw", tone === "danger" ? "timeline_error" : "timeline");
  const rawLink = buildTraceLink(item.trace_id, "", sessionID, "raw", focus === "failure" ? "response" : focus);

  return (
    <div className={`failure-node failure-node-${tone}`}>
      <div className="failure-node-label">{label}</div>
      <div className="trace-tag-group">
        <InlineTag tone={tone === "danger" ? "danger" : tone === "accent" ? "accent" : "default"}>{formatEndpointTag(item.endpoint)}</InlineTag>
        <InlineTag>{item.status_code}</InlineTag>
      </div>
      <strong>{item.model || t("sessionDetail.unknownModel")}</strong>
      <span>{formatDateTime(item.time)}</span>
      <span>duration {formatDuration(item.duration_ms)}</span>
      <span>tokens {formatTokenCount(item.total_tokens)}</span>
      <span>rate {formatTokenRate(item.total_tokens, item.duration_ms)}</span>
      {delta ? (
        <div className="failure-delta-row">
          <span>vs prev duration {formatSignedMetric(delta.duration_ms / 1000)}s</span>
          <span>tokens {formatSignedMetric(delta.total_tokens)}</span>
        </div>
      ) : null}
      {detail ? <div className="failure-node-detail">{detail}</div> : null}
      <div className="action-group action-group-start">
        <Button asChild variant="ghost" to={timelineLink}>
          <Link to={timelineLink}>
            {t("requests.timeline")}
          </Link>
        </Button>
        <Button asChild variant="ghost" to={rawLink}>
          <Link to={rawLink}>
            {t("requests.raw")}
          </Link>
        </Button>
        <Button asChild variant="default" size="icon">
          <Link to={traceLink} title={t("requests.viewTrace")} aria-label={t("requests.viewTrace")}>
            <ViewIcon />
          </Link>
        </Button>
      </div>
    </div>
  );
}
