import React, { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { StatCard } from "../components/common/Display";
import { WindowToggle } from "../components/common/Tabs";
import { DetailMetaPill, HomeIcon, InlineTag, TokenBadge } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { BreakdownList } from "../components/monitor/BreakdownList";
import { RequestList } from "../components/monitor/RequestList";
import { RoutingFailureTimeline } from "../components/monitor/RoutingFailureTimeline";
import { useJSON } from "../hooks/useJSON";
import { apiPaths, apiURL } from "../lib/api";
import { useI18n } from "../lib/i18n";
import {
  buildRoutingLink,
  buildTraceLink,
  computeTTFTRatio,
  formatCapacity,
  formatDateTime,
  formatDuration,
  formatEndpointTag,
  formatFailureReason,
  formatHealthLabel,
  formatMultiplier,
  formatRatio,
  formatTime,
  healthTone,
  metricThresholdTone,
  normalizeUpstreamWindow,
  resolveThresholdState,
  setOrDeleteParam,
} from "../lib/monitor";

export function UpstreamDetailPage() {
  const { upstreamID = "" } = useParams();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeUpstreamWindow(searchParams.get("window"));
  const modelValue = searchParams.get("model") || "";
  const [modelDraft, setModelDraft] = useState(modelValue);
  const [catalogQuery, setCatalogQuery] = useState("");
  const params = new URLSearchParams();
  params.set("window", windowValue);
  if (modelValue) {
    params.set("model", modelValue);
  }
  const detail = useJSON(apiURL(apiPaths.upstream(upstreamID), params), [upstreamID, windowValue, modelValue]);
  const target = detail.data?.target;
  const breakdown = detail.data?.breakdown;
  const traces = detail.data?.traces ?? [];
  const timeline = detail.data?.timeline ?? [];
  const failureTimeline = detail.data?.failure_timeline ?? [];
  const thresholds = detail.data?.health_thresholds;
  const topFailureReason = breakdown?.failure_reasons?.[0]?.label || "";
  const catalogModels = target?.models || [];
  const recentModels = target?.recent_models || [];
  const normalizedCatalogQuery = catalogQuery.trim().toLowerCase();
  const visibleCatalogModels = normalizedCatalogQuery
    ? catalogModels.filter((model) => model.toLowerCase().includes(normalizedCatalogQuery))
    : catalogModels;
  const visibleRecentModels = normalizedCatalogQuery
    ? recentModels.filter((model) => model.toLowerCase().includes(normalizedCatalogQuery))
    : recentModels;

  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    setSearchParams(next);
  };
  const applyModel = (event) => {
    event.preventDefault();
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "model", modelDraft);
    setSearchParams(next);
  };

  useEffect(() => {
    setModelDraft(modelValue);
  }, [modelValue]);

  return (
    <div className="shell shell-detail">
      <header className="topbar detail-topbar">
        <div className="detail-title-block">
          <div className="detail-heading-row">
            <h1>{target?.id || upstreamID || t("upstreamDetail.upstreamFallback")}</h1>
            <div className="trace-tag-group detail-tag-group">
              <InlineTag tone={healthTone(target?.health_state)}>{formatHealthLabel(target?.health_state)}</InlineTag>
              <InlineTag tone="accent">{target?.provider_preset || "custom"}</InlineTag>
              <InlineTag>{target?.routing_profile || target?.protocol_family || "route"}</InlineTag>
            </div>
          </div>
          <div className="detail-meta-strip">
            <DetailMetaPill label={t("upstreamDetail.baseUrl")} value={target?.base_url || "-"} mono />
            <DetailMetaPill label={t("upstreamDetail.lastSeen")} value={formatDateTime(target?.last_seen)} />
            <DetailMetaPill label={t("common.requests")} value={target?.request_count ?? 0} />
            <DetailMetaPill label={t("common.success")} value={`${Number(target?.success_rate || 0).toFixed(1)}%`} />
          </div>
        </div>
        <div className="topbar-meta detail-toolbar">
          <div className="detail-toolbar-actions">
            <Link className="icon-button" to={buildRoutingLink(windowValue, modelValue)} title={t("upstreamDetail.backToRouting")} aria-label={t("upstreamDetail.backToRouting")}>
              <HomeIcon />
            </Link>
          </div>
          <div className="detail-toolbar-tokens">
            <TokenBadge label={t("metric.ttft")} value={target?.avg_ttft ?? 0} icon="duration" format="duration" />
            <TokenBadge label={t("metric.totalTokens")} value={target?.total_tokens ?? 0} icon="total" accent="token-badge-strong" />
            <TokenBadge label={t("common.failed")} value={target?.failed_request ?? 0} icon="failed" />
          </div>
        </div>
      </header>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("upstreamDetail.analyticsFilters")}</p>
            <h2>{t("upstreamDetail.windowAndModel")}</h2>
          </div>
          <div className="panel-head-actions">
            <WindowToggle value={windowValue} onChange={setWindow} label={t("upstreamDetail.windowLabel")} />
            <span className="badge">{detail.data?.refreshed_at ? formatTime(detail.data.refreshed_at) : "..."}</span>
          </div>
        </div>
        <form className="filter-bar" onSubmit={applyModel}>
          <input
            className="filter-input filter-input-wide"
            type="search"
            name="model"
            value={modelDraft}
            onChange={(event) => setModelDraft(event.target.value)}
            placeholder={t("upstreamDetail.filterByModel")}
          />
          <button className="ghost-button" type="submit">{t("common.apply")}</button>
          <button
            className="ghost-button"
            type="button"
            onClick={() => {
              setModelDraft("");
              const next = new URLSearchParams(searchParams);
              next.delete("model");
              setSearchParams(next);
            }}
          >
            {t("common.reset")}
          </button>
        </form>
      </section>

      {detail.error ? <EmptyState title={t("upstreamDetail.loadError")} detail={detail.error} tone="danger" /> : null}
      {detail.loading && !detail.data ? <EmptyState title={t("upstreamDetail.loading")} detail={t("upstreamDetail.loadingDetail")} /> : null}

      {detail.data ? (
        <div className="detail-grid detail-grid-compact">
          <section className="panel">
            <div className="panel-head">
              <div>
                <p className="eyebrow">{t("upstreamDetail.trafficSummary")}</p>
                <h2>{t("upstreamDetail.routingHealth")}</h2>
              </div>
            </div>
            <div className="hero-grid hero-grid-compact">
              <StatCard label={t("common.requests")} value={target?.request_count ?? 0} />
              <StatCard label={t("common.failed")} value={breakdown?.failed_traces ?? 0} accent={(breakdown?.failed_traces ?? 0) > 0 ? "accent-red" : ""} />
              <StatCard label={t("upstreamDetail.inflight")} value={target?.inflight ?? 0} />
              <StatCard label={t("providers.capacity")} value={formatCapacity(target?.weight, target?.capacity_hint)} />
            </div>
          </section>
          <section className="panel">
            <div className="panel-head">
              <div>
                <p className="eyebrow">{t("upstreamDetail.routerHealth")}</p>
                <h2>{t("upstreamDetail.decisionSignals")}</h2>
              </div>
            </div>
            <div className="session-breakdown-grid">
              <section className="breakdown-card">
                <div className="breakdown-title">{t("upstreamDetail.healthState")}</div>
                <div className="routing-summary-stack">
                  <strong className="trace-model-name">{formatHealthLabel(target?.health_state)}</strong>
                  <div className="trace-tag-group">
                    <InlineTag tone={healthTone(target?.health_state)}>{formatHealthLabel(target?.health_state)}</InlineTag>
                    {topFailureReason ? <InlineTag tone="accent">{formatFailureReason(topFailureReason)}</InlineTag> : null}
                  </div>
                  <span className="trace-subline">{buildUpstreamHealthSummary(target, breakdown?.failure_reasons || [], thresholds)}</span>
                </div>
              </section>
              <section className="breakdown-card">
                <div className="breakdown-title">{t("upstreamDetail.liveMetrics")}</div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("upstreamDetail.error")} value={formatRatio(target?.error_rate)} />
                  <DetailMetaPill label={t("upstreamDetail.timeout")} value={formatRatio(target?.timeout_rate)} />
                  <DetailMetaPill label="ttft" value={formatDuration(target?.ttft_fast_ms || target?.avg_ttft || 0)} />
                  <DetailMetaPill label={t("upstreamDetail.latency")} value={formatDuration(target?.latency_fast_ms || 0)} />
                  <DetailMetaPill label={t("upstreamDetail.refresh")} value={target?.last_refresh_status || "unknown"} />
                </div>
              </section>
              <section className="breakdown-card">
                <div className="breakdown-title">{t("upstreamDetail.thresholdChecks")}</div>
                <div className="breakdown-list">
                  <div className="breakdown-row">
                    <span className="breakdown-label">{t("upstreamDetail.errorRate")}</span>
                    <div className="trace-tag-group">
                      <InlineTag tone={metricThresholdTone(resolveThresholdState(target?.error_rate, thresholds?.error_rate_degraded, thresholds?.error_rate_open))}>
                        {resolveThresholdState(target?.error_rate, thresholds?.error_rate_degraded, thresholds?.error_rate_open)}
                      </InlineTag>
                      <strong>{formatRatio(target?.error_rate)} / {formatRatio(thresholds?.error_rate_degraded)} / {formatRatio(thresholds?.error_rate_open)}</strong>
                    </div>
                  </div>
                  <div className="breakdown-row">
                    <span className="breakdown-label">{t("upstreamDetail.timeoutRate")}</span>
                    <div className="trace-tag-group">
                      <InlineTag tone={metricThresholdTone(resolveThresholdState(target?.timeout_rate, thresholds?.timeout_rate_degraded, thresholds?.timeout_rate_open))}>
                        {resolveThresholdState(target?.timeout_rate, thresholds?.timeout_rate_degraded, thresholds?.timeout_rate_open)}
                      </InlineTag>
                      <strong>{formatRatio(target?.timeout_rate)} / {formatRatio(thresholds?.timeout_rate_degraded)} / {formatRatio(thresholds?.timeout_rate_open)}</strong>
                    </div>
                  </div>
                  <div className="breakdown-row">
                    <span className="breakdown-label">{t("upstreamDetail.ttftRatio")}</span>
                    <div className="trace-tag-group">
                      <InlineTag tone={metricThresholdTone(resolveThresholdState(computeTTFTRatio(target), thresholds?.ttft_degraded_ratio, null))}>
                        {resolveThresholdState(computeTTFTRatio(target), thresholds?.ttft_degraded_ratio, null)}
                      </InlineTag>
                      <strong>{formatMultiplier(computeTTFTRatio(target))} / {formatMultiplier(thresholds?.ttft_degraded_ratio)}</strong>
                    </div>
                  </div>
                  <div className="breakdown-row">
                    <span className="breakdown-label">{t("upstreamDetail.routerGates")}</span>
                    <strong>{t("upstreamDetail.routerGateValue", { failures: thresholds?.failure_threshold ?? 0, window: thresholds?.open_window || "-" })}</strong>
                  </div>
                </div>
              </section>
            </div>
          </section>
          <section className="panel">
            <div className="panel-head">
              <div>
                <p className="eyebrow">{t("upstreamDetail.distribution")}</p>
                <h2>{t("upstreamDetail.modelsAndEndpoints")}</h2>
              </div>
            </div>
            <div className="session-breakdown-grid">
              <BreakdownList title={t("upstreamDetail.models")} items={breakdown?.models || []} formatter={(item) => item.label} />
              <BreakdownList title={t("upstreamDetail.endpoints")} items={breakdown?.endpoints || []} formatter={(item) => formatEndpointTag(item.label)} />
            </div>
          </section>
          <section className="panel" id="models">
            <div className="panel-head">
              <div>
                <p className="eyebrow">{t("upstreamDetail.modelCatalog")}</p>
                <h2>{t("upstreamDetail.fullRoutingSurface")}</h2>
              </div>
              <div className="panel-head-actions">
                <span className="session-filter-count">
                  {t("upstreamDetail.indexedCount", { visible: visibleCatalogModels.length, total: catalogModels.length })}
                </span>
                <span className="session-filter-count">
                  {t("upstreamDetail.recentCount", { visible: visibleRecentModels.length, total: recentModels.length })}
                </span>
              </div>
            </div>
            <form className="filter-bar" onSubmit={(event) => event.preventDefault()}>
              <input
                className="filter-input filter-input-wide"
                type="search"
                value={catalogQuery}
                onChange={(event) => setCatalogQuery(event.target.value)}
                placeholder={t("upstreamDetail.searchModels")}
              />
            </form>
            <div className="session-breakdown-grid">
              <section className="breakdown-card">
                <div className="breakdown-title">{t("upstreamDetail.recentlyRoutedModels")}</div>
                {visibleRecentModels.length ? (
                  <div className="model-catalog-list">
                    {visibleRecentModels.map((model) => (
                      <div key={`recent-${model}`} className="model-catalog-row" title={model}>
                        <strong>{model}</strong>
                        {target?.last_model === model ? <InlineTag tone="accent">{t("upstreamDetail.lastTag")}</InlineTag> : null}
                      </div>
                    ))}
                  </div>
                ) : (
                  <EmptyState title={t("upstreamDetail.noRecentModels")} detail={t("upstreamDetail.noRecentModelsDetail")} compact />
                )}
              </section>
              <section className="breakdown-card">
                <div className="breakdown-title">{t("upstreamDetail.indexedModels")}</div>
                {visibleCatalogModels.length ? (
                  <div className="model-catalog-list">
                    {visibleCatalogModels.map((model) => (
                      <div key={`catalog-${model}`} className="model-catalog-row" title={model}>
                        <strong>{model}</strong>
                      </div>
                    ))}
                  </div>
                ) : (
                  <EmptyState title={t("upstreamDetail.noIndexedModels")} detail={t("upstreamDetail.noIndexedModelsDetail")} compact />
                )}
              </section>
            </div>
          </section>
        </div>
      ) : null}

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("upstreamDetail.failureTrend")}</p>
            <h2>{t("upstreamDetail.timeBucketedFailures")}</h2>
          </div>
        </div>
        {failureTimeline.length ? (
          <RoutingFailureTimeline items={failureTimeline} />
        ) : (
          <EmptyState title={t("upstreamDetail.noFailureTimeline")} detail={t("upstreamDetail.noFailureTimelineDetail")} />
        )}
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("upstreamDetail.recentFailures")}</p>
            <h2>{t("upstreamDetail.latestFailedTraces")}</h2>
          </div>
        </div>
        {timeline.length ? (
          <div className="upstream-failure-list upstream-failure-list-detail">
            {timeline.map((failure) => (
              <Link key={failure.trace_id} className="upstream-failure-card" to={buildTraceLink(failure.trace_id, "requests", "", "", "failure")}>
                <div className="trace-tag-group">
                  <InlineTag tone="danger">{failure.status_code}</InlineTag>
                  <InlineTag tone="accent">{formatEndpointTag(failure.endpoint)}</InlineTag>
                  {failure.reason ? <InlineTag>{formatFailureReason(failure.reason)}</InlineTag> : null}
                </div>
                <strong>{failure.model || t("upstreamDetail.unknownModel")}</strong>
                <span>{formatDateTime(failure.recorded_at)}</span>
                {failure.error_text ? <div className="upstream-failure-detail">{failure.error_text}</div> : null}
              </Link>
            ))}
          </div>
        ) : (
          <EmptyState title={t("upstreamDetail.noRecentFailures")} detail={t("upstreamDetail.noRecentFailuresDetail")} />
        )}
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("upstreamDetail.recentRequests")}</p>
            <h2>{t("upstreamDetail.latestRoutedTraces")}</h2>
          </div>
        </div>
        {traces.length ? <RequestList items={traces} focusFailures /> : <EmptyState title={t("upstreamDetail.noRoutedTraces")} detail={t("upstreamDetail.noRoutedTracesDetail")} />}
      </section>
    </div>
  );
}

function buildUpstreamHealthSummary(target, failureReasons = [], thresholds = null) {
  const health = formatHealthLabel(target?.health_state || "unknown");
  const errorRate = formatRatio(target?.error_rate);
  const timeoutRate = formatRatio(target?.timeout_rate);
  const topReason = failureReasons[0]?.label ? formatFailureReason(failureReasons[0].label) : "no dominant failure reason";
  const signals = [];
  const errorState = resolveThresholdState(target?.error_rate, thresholds?.error_rate_degraded, thresholds?.error_rate_open);
  if (errorState !== "healthy" && errorState !== "unknown") {
    signals.push(`error ${errorState}`);
  }
  const timeoutState = resolveThresholdState(target?.timeout_rate, thresholds?.timeout_rate_degraded, thresholds?.timeout_rate_open);
  if (timeoutState !== "healthy" && timeoutState !== "unknown") {
    signals.push(`timeout ${timeoutState}`);
  }
  const ttftState = resolveThresholdState(computeTTFTRatio(target), thresholds?.ttft_degraded_ratio, null);
  if (ttftState !== "healthy" && ttftState !== "unknown") {
    signals.push(`ttft ${ttftState}`);
  }
  const signalText = signals.length ? ` Thresholds: ${signals.join(", ")}.` : "";
  return `${health} with error ${errorRate}, timeout ${timeoutRate}, dominant failure ${topReason}.${signalText}`;
}
