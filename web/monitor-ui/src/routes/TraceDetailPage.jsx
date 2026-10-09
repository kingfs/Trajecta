import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "../components/ui/dialog";
import React, { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { CollapsibleCard, CodeBlock, MessageContent, StatCard } from "../components/common/Display";
import { DetailMetaPill, DownloadIcon, HomeIcon, InlineTag, StackIcon, TokenBadge } from "../components/common/Badges";
import { EmptyState } from "../components/common/EmptyState";
import { useJSON } from "../hooks/useJSON";
import { apiPaths, downloadBlob, postJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import {
  buildRoutingDecisionSummary,
  buildProviderLink,
  buildTraceLink,
  buildTraceUpstreamHealthSummary,
  buildUpstreamLink,
  formatDateTime,
  formatDuration,
  formatCacheRate,
  formatRawCacheRate,
  formatEndpointTag,
  formatFailureReason,
  formatHealthLabel,
  formatProviderTag,
  formatRatio,
  formatTokenCount,
  formatRoutingScore,
  formatTokenRate,
  healthTone,
  metricThresholdTone,
  normalizeTraceTab,
  resolveThresholdState,
  setOrDeleteParam,
  summarizeTraceFailure,
} from "../lib/monitor";
import {
  buildToolMessageSummary,
  buildToolSchemaSummary,
  collectTraceToolCalls,
  countToolMatches,
  findDeclaredToolForCall,
  normalizeDeclaredTool,
  isSameToolName,
} from "../lib/traceTools";

export function TraceDetailPage() {
  const { traceID = "" } = useParams();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = normalizeTraceTab(searchParams.get("tab"));
  const [renderMarkdown, setRenderMarkdown] = useState(true);

  const [derivedRefreshTick, setDerivedRefreshTick] = useState(0);
  const failureSummaryRef = useRef(null);
  const detail = useJSON(apiPaths.trace(traceID), [traceID]);
  // The sub-resources all live under the same trace: when the main lookup fails
  // (an unknown id returns 404) there is nothing to load, so they stay parked
  // instead of firing four more doomed requests at the backend.
  const traceExists = Boolean(detail.data);
  const raw = useJSON(traceExists ? apiPaths.traceRaw(traceID) : null, [traceID, tab === "raw" ? "raw" : "summary"]);
  const observation = useJSON(traceExists ? apiPaths.traceObservation(traceID) : null, [traceID, tab === "protocol" ? "protocol" : "idle", derivedRefreshTick]);
  const findings = useJSON(traceExists ? apiPaths.traceFindings(traceID) : null, [traceID, tab === "audit" ? "audit" : "idle", derivedRefreshTick]);
  const performance = useJSON(traceExists ? apiPaths.tracePerformance(traceID) : null, [traceID, tab === "performance" ? "performance" : "idle"]);
  const header = detail.data?.header?.meta;
  const usage = detail.data?.header?.usage;
  const session = detail.data?.session;
  const failureSummary = summarizeTraceFailure(detail.data);
  const selectedUpstreamID = header?.selected_upstream_id || "";
  const selectedUpstreamBaseURL = header?.selected_upstream_base_url || "";
  const selectedUpstreamProviderPreset = header?.selected_upstream_provider_preset || "";
  const routingPolicy = header?.routing_policy || "";
  const routingScore = Number(header?.routing_score || 0);
  const routingCandidateCount = Number(header?.routing_candidate_count || 0);
  const routingFailureReason = header?.routing_failure_reason || "";
  const selectedUpstreamHealth = detail.data?.selected_upstream_health;
  const declaredTools = (detail.data?.tools || []).map((tool, index) => normalizeDeclaredTool(tool, index)).filter((tool) => tool.name || tool.description || tool.parameters);
  const traceToolCalls = collectTraceToolCalls(detail.data);
  const focusTarget = searchParams.get("focus") || "";
  const hasDeclaredToolsTab = Boolean(declaredTools.length);
  const fromSessionID = searchParams.get("from_session") || "";
  const fromView = searchParams.get("view") === "sessions" ? "sessions" : "requests";
  // The request list and the session list are two tabs of /traces now, so a
  // trace opened from either one comes back to the right tab.
  const backLink = fromSessionID
    ? `/sessions/${encodeURIComponent(fromSessionID)}`
    : fromView === "sessions"
      ? "/traces?tab=sessions"
      : "/traces";
  const conversation = hasConversation(detail.data);
  const timelineCount = detail.data?.events?.length || 0;
  const messageCount = detail.data?.messages?.length || 0;
  const toolCount = declaredTools.length;
  const routingDecision = buildRoutingDecision(detail.data?.events || []);
  const routePlan = buildRoutePlan(detail.data?.events || []);
  const activeRouting = routePlan || routingDecision;
  const selectedRouteIdentity = activeRouting.selectedRouteTargetID || activeRouting.selectedUpstreamID || selectedUpstreamID;
  const selectedChannelID = activeRouting.selectedChannelID || selectedUpstreamID;
  const responsesAuditLink = buildResponsesAuditLink(detail.data);
  const upstreamCalls = Array.isArray(detail.data?.upstream_calls) ? detail.data.upstream_calls : [];

  const applyTraceFocus = (nextTab, nextFocus = "") => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "tab", nextTab === "conversation" ? "" : nextTab);
    setOrDeleteParam(next, "focus", nextFocus);
    setSearchParams(next, { replace: true });
  };

  const setTraceTab = (nextTab) => {
    applyTraceFocus(nextTab, focusTarget);
  };

  const downloadTrace = async () => {
    let blob;
    try {
      blob = await downloadBlob(apiPaths.traceDownload(traceID));
    } catch {
      return;
    }
    const url = window.URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${traceID}.http`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.URL.revokeObjectURL(url);
  };

  // The action name is the mutation's own variable now, so the toolbar can still
  // tell "repair" from "reanalyze" while one of them is in flight.
  const runTraceAction = useWriteMutation({
    mutationFn: ({ path, payload }) => postJSON(path, payload),
    error: (err, variables) => `${labelTraceAction(variables.action, t)} failed: ${err?.message || t("common.actionFailed")}`,
    onSuccess: (response, variables) => {
      // The request succeeded either way; whether the job it queued failed is a
      // property of the result, which is why this picks its own toast.
      const text = `${labelTraceAction(variables.action, t)} job #${response.job?.id || "-"} ${response.job?.status || "queued"}`;
      if (response.job?.status === "failed") {
        toast.error(text);
      } else {
        toast.success(text);
      }
      setDerivedRefreshTick((value) => value + 1);
    },
  });
  const jobBusy = runTraceAction.isPending ? runTraceAction.variables?.action || "" : "";
  const runTraceActionWith = (action, path, payload = {}) => runTraceAction.mutate({ action, path, payload });

  useEffect(() => {
    if (focusTarget !== "failure" || !failureSummary || !failureSummaryRef.current) {
      return;
    }
    failureSummaryRef.current.scrollIntoView({ block: "start", behavior: "smooth" });
  }, [failureSummary, focusTarget]);

  return (
    <div className="shell shell-detail">
      <header className="topbar detail-topbar">
        <div className="detail-title-block">
          <div className="detail-heading-row">
            <h1>{header?.model || t("traceDetail.traceFallback")}</h1>
            <div className="trace-tag-group detail-tag-group">
              <InlineTag tone="accent">{formatEndpointTag(header?.endpoint || header?.operation)}</InlineTag>
              <InlineTag>{formatProviderTag(header?.provider)}</InlineTag>
              {selectedUpstreamID ? (
                <span title={selectedUpstreamID}>
                  <InlineTag tone="green">{selectedUpstreamProviderPreset || compactUpstreamID(selectedUpstreamID)}</InlineTag>
                </span>
              ) : null}
              {detail.data?.header?.layout?.is_stream ? <InlineTag tone="gold">{t("routing.stream")}</InlineTag> : null}
              <InlineTag tone={header?.status_code >= 200 && header?.status_code < 300 ? "green" : "danger"}>{header?.status_code || 0}</InlineTag>
            </div>
          </div>
          <div className="detail-meta-strip">
            {session?.session_id ? <DetailMetaPill label={t("traceDetail.metaSession")} value={session.session_id} mono /> : null}
            <DetailMetaPill label={t("traceDetail.metaTime")} value={formatDateTime(header?.time)} />
            <DetailMetaPill label={t("traceDetail.metaEndpoint")} value={header?.endpoint || header?.url || "-"} />
            <DetailMetaPill label={t("sessions.duration")} value={formatDuration(header?.duration_ms || 0, { precise: true })} />
            <DetailMetaPill label="ttft" value={formatDuration(header?.ttft_ms || 0, { precise: true })} />
            <DetailMetaPill label={t("traceDetail.metaRate")} value={formatTokenRate(usage?.total_tokens || 0, header?.duration_ms || 0)} />
            <DetailMetaPill label={t("traceDetail.metaRequestID")} value={header?.request_id || "-"} mono />
          </div>
        </div>
        <div className="topbar-meta detail-toolbar">
          <div className="detail-toolbar-actions">
            <Link className="icon-button" to={backLink} title={fromSessionID ? t("traceDetail.backToSession") : t("traceDetail.backToList")} aria-label={fromSessionID ? t("traceDetail.backToSession") : t("traceDetail.backToList")}>
              <HomeIcon />
            </Link>
            {session?.session_id ? (
              <Link className="icon-button" to={`/sessions/${encodeURIComponent(session.session_id)}`} title={t("requests.viewSession")} aria-label={t("requests.viewSession")}>
                <StackIcon />
              </Link>
            ) : null}
            <button className="icon-button" type="button" onClick={downloadTrace} title={t("traceDetail.downloadHttp")} aria-label={t("requests.downloadTrace")}>
              <DownloadIcon />
            </button>
          </div>
          <div className="detail-toolbar-actions trace-reanalysis-actions">
            <button className="ghost-button" type="button" disabled={!traceExists || jobBusy === "repair"} onClick={() => runTraceActionWith("repair", apiPaths.traceRepairUsage(traceID), { mode: "sync" })}>
              {jobBusy === "repair" ? t("traceDetail.repairing") : t("traceDetail.repairStats")}
            </button>
            <button className="ghost-button active" type="button" disabled={!traceExists || jobBusy === "reanalyze"} onClick={() => runTraceActionWith("reanalyze", apiPaths.traceReanalyze(traceID), { mode: "sync" })}>
              {jobBusy === "reanalyze" ? t("traceDetail.reanalyzing") : t("traceDetail.reanalyze")}
            </button>
          </div>
          <div className="detail-toolbar-tokens">
            <TokenBadge label={t("metric.inputTokens")} value={usage?.prompt_tokens || 0} icon="input" />
            <TokenBadge label={t("metric.outputTokens")} value={usage?.completion_tokens || 0} icon="output" />
            <TokenBadge label={t("metric.totalTokens")} value={usage?.total_tokens || 0} icon="total" accent="token-badge-strong" />
            <TokenBadge label={t("metric.cachedTokens")} value={usage?.prompt_tokens_details?.cached_tokens || 0} icon="cached" />
            <TokenBadge label={t("metric.cacheRate")} value={formatCacheRate(usage?.prompt_tokens_details?.cached_tokens || 0, usage?.total_tokens || 0)} raw={formatRawCacheRate(usage?.prompt_tokens_details?.cached_tokens || 0, usage?.total_tokens || 0)} icon="percent" />
          </div>
        </div>
      </header>

      {failureSummary ? (
        <section
          ref={failureSummaryRef}
          className={focusTarget === "failure" ? "panel trace-failure-panel trace-failure-panel-focused" : "panel trace-failure-panel"}
        >
          <div className="trace-failure-head">
            <div>
              <p className="eyebrow">{t("traceDetail.failureSummary")}</p>
              <h2>{failureSummary.title}</h2>
            </div>
            <InlineTag tone="danger">{header?.status_code || 0}</InlineTag>
          </div>
          <p className="trace-failure-summary">{failureSummary.summary}</p>
          <div className="trace-failure-meta">
            <span>{header?.endpoint || header?.url || "-"}</span>
            <span>{t("sessions.duration")} {formatDuration(header?.duration_ms || 0, { precise: true })}</span>
            <span>ttft {formatDuration(header?.ttft_ms || 0, { precise: true })}</span>
            <span>{t("traceDetail.tokensLabel")} {formatTokenCount(usage?.total_tokens || 0)}</span>
            <span>{t("traceDetail.metaRate")} {formatTokenRate(usage?.total_tokens || 0, header?.duration_ms || 0)}</span>
          </div>
          <div className="trace-failure-actions">
            <button className={tab === "conversation" ? "ghost-button active" : "ghost-button"} onClick={() => applyTraceFocus("conversation", "timeline_error")}>
              {t("traceDetail.openConversation")}
            </button>
            <button className={tab === "raw" ? "ghost-button active" : "ghost-button"} onClick={() => applyTraceFocus("raw", "response")}>
              {t("traceDetail.openRawProtocol")}
            </button>
            {session?.session_id ? (
              <Link className="ghost-button" to={`/sessions/${encodeURIComponent(session.session_id)}`}>
                {t("traceDetail.backToSession")}
              </Link>
            ) : null}
          </div>
          {failureSummary.detail ? <pre className="trace-failure-detail">{failureSummary.detail}</pre> : null}
        </section>
      ) : null}

      {detail.data ? (
        <section className="panel trace-reading-panel">
          <div className="panel-head">
            <div>
              <p className="eyebrow">{t("traceDetail.readingGuide")}</p>
              <h2>{t("traceDetail.traceInspector")}</h2>
            </div>
            {responsesAuditLink ? (
              <div className="panel-head-actions">
                <Link className="ghost-button active" to={responsesAuditLink}>
                  {t("traceDetail.responsesAudit")}
                </Link>
              </div>
            ) : null}
          </div>
          <div className="trace-reading-grid">
            <button className={tab === "conversation" ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTraceTab("conversation")}>
              <strong>{t("traceDetail.cardConversation")}</strong>
              <span>{conversation ? t(messageCount > 1 ? "traceDetail.capturedMessages" : "traceDetail.capturedMessagesOne", { count: messageCount }) : t(timelineCount > 1 ? "traceDetail.eventRecords" : "traceDetail.eventRecordsOne", { count: timelineCount })}</span>
              <p>{t("traceDetail.cardConversationDetail")}</p>
            </button>
            <button className={tab === "protocol" ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTraceTab("protocol")}>
              <strong>{t("audit.protocol")}</strong>
              <span>{t("traceDetail.observationIR")}</span>
              <p>{t("traceDetail.cardProtocolDetail")}</p>
            </button>
            <button className={tab === "audit" ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTraceTab("audit")}>
              <strong>{t("nav.audit")}</strong>
              <span>{t("traceDetail.deterministicFindings")}</span>
              <p>{t("traceDetail.cardAuditDetail")}</p>
            </button>
            <button className={tab === "performance" ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTraceTab("performance")}>
              <strong>{t("traceDetail.performanceTitle")}</strong>
              <span>{t("traceDetail.latencyAndTokenSpeed")}</span>
              <p>{t("traceDetail.cardPerformanceDetail")}</p>
            </button>
            <button className={tab === "raw" ? "trace-reading-card trace-reading-card-active" : "trace-reading-card"} onClick={() => setTraceTab("raw")}>
              <strong>{t("requests.raw")}</strong>
              <span>{t("traceDetail.originalHttpExchange")}</span>
              <p>{t("traceDetail.cardRawDetail")}</p>
            </button>
          </div>
        </section>
      ) : null}

      {detail.error ? <EmptyState title={t("traceDetail.loadError")} detail={detail.error} tone="danger" /> : null}
      {detail.loading && !detail.data ? <EmptyState title={t("traceDetail.loading")} detail={t("traceDetail.loadingDetail")} /> : null}

      {tab === "conversation" && detail.data ? (
        <div className="detail-grid">
          {selectedUpstreamID || routingFailureReason || routingDecision.events.length ? (
            <section className="panel">
              <div className="panel-head">
                <div>
                  <p className="eyebrow">{routePlan ? t("traceDetail.routePlan") : t("traceDetail.routingDecision")}</p>
                  <h2>{selectedRouteIdentity ? t("traceDetail.selectedRouteTarget") : t("traceDetail.routingFailure")}</h2>
                </div>
                <div className="panel-head-actions">
                  {selectedChannelID ? (
                    <Link className="ghost-button active" to={buildProviderLink(selectedChannelID)}>
                      {t("traceDetail.openChannel")}
                    </Link>
                  ) : null}
                  {selectedUpstreamID ? (
                    <Link className="ghost-button" to={buildUpstreamLink(selectedUpstreamID)}>
                      {t("traceDetail.openUpstream")}
                    </Link>
                  ) : null}
                </div>
              </div>
              <div className="detail-meta-strip">
                <DetailMetaPill label={t("traceDetail.metaRouteTarget")} value={selectedRouteIdentity || "-"} mono />
                <DetailMetaPill label={t("traceDetail.metaChannel")} value={selectedChannelID || "-"} mono />
                {routePlan?.selectedUpstreamID ? <DetailMetaPill label={t("traceDetail.metaUpstream")} value={routePlan.selectedUpstreamID} mono /> : null}
                {routingDecision.selectedCredentialID && !routePlan ? <DetailMetaPill label={t("traceDetail.metaCredential")} value={routingDecision.selectedCredentialID} mono /> : null}
                {routingDecision.selectedCredentialHint && !routePlan ? <DetailMetaPill label={t("traceDetail.metaHint")} value={routingDecision.selectedCredentialHint} mono /> : null}
                <DetailMetaPill label={t("providers.providerFallback")} value={selectedUpstreamProviderPreset || "-"} />
                {routePlan ? <DetailMetaPill label={t("traceDetail.metaEntrypoint")} value={routePlan.clientEntrypoint || "-"} /> : null}
                {routePlan ? <DetailMetaPill label={t("traceDetail.metaMode")} value={formatRoutePlanValue(routePlan.executionMode)} /> : null}
                {routePlan ? <DetailMetaPill label={t("traceDetail.metaStrategy")} value={formatRoutePlanValue(routePlan.strategy || routingPolicy)} /> : <DetailMetaPill label={t("traceDetail.metaPolicy")} value={routingPolicy || "-"} />}
                {!routePlan ? <DetailMetaPill label={t("traceDetail.metaScore")} value={formatRoutingScore(routingScore)} /> : null}
                <DetailMetaPill label={t("traceDetail.metaCandidates")} value={routePlan ? routePlan.candidateSummary.length : routingCandidateCount || 0} />
                {(routePlan?.failureReason || routingFailureReason) ? <DetailMetaPill label={t("traceDetail.metaFailure")} value={formatFailureReason(routePlan?.failureReason || routingFailureReason)} /> : null}
              </div>
              {routePlan ? (
                <RoutePlanSummary plan={routePlan} selectedUpstreamBaseURL={selectedUpstreamBaseURL} selectedUpstreamProviderPreset={selectedUpstreamProviderPreset} InlineTag={InlineTag} t={t} />
              ) : (
                <div className="routing-summary-grid">
                  <section className="breakdown-card">
                    <div className="breakdown-title">{selectedRouteIdentity ? t("traceDetail.resolvedRouteTarget") : t("traceDetail.failureClass")}</div>
                    <div className="routing-summary-stack">
                      <strong className="trace-model-name">{selectedRouteIdentity || formatFailureReason(routingFailureReason) || t("traceDetail.routingFailure")}</strong>
                      <span className="trace-subline mono">{selectedUpstreamBaseURL || "-"}</span>
                      {selectedRouteIdentity || routingPolicy ? (
                        <div className="trace-tag-group">
                          {selectedUpstreamProviderPreset ? <InlineTag tone="accent">{selectedUpstreamProviderPreset}</InlineTag> : null}
                          {routingDecision.selectedCredentialID ? <InlineTag tone="gold">{routingDecision.selectedCredentialID}</InlineTag> : null}
                          {routingPolicy ? <InlineTag>{routingPolicy}</InlineTag> : null}
                        </div>
                      ) : null}
                    </div>
                  </section>
                  <section className="breakdown-card">
                    <div className="breakdown-title">{t("traceDetail.decisionExplanation")}</div>
                    <div className="routing-summary-stack">
                      <span className="trace-subline">
                        {buildRoutingDecisionSummary({
                          upstreamID: selectedRouteIdentity,
                          policy: routingPolicy,
                          score: routingScore,
                          candidateCount: routingCandidateCount,
                          failureReason: routingFailureReason,
                        })}
                      </span>
                    </div>
                  </section>
                </div>
              )}
              {routingDecision.stickyBreaks.length || selectedUpstreamHealth ? (
                <div className="routing-summary-grid">
                  {routingDecision.stickyBreaks.length ? (
                    <section className="breakdown-card">
                      <div className="breakdown-title">{t("traceDetail.stickyCredentialBreak")}</div>
                      <div className="routing-summary-stack">
                        {routingDecision.stickyBreaks.map((event, index) => (
                          <div className="credential-break-row" key={`sticky-break-${index}`}>
                            <div className="trace-tag-group">
                              <InlineTag tone="danger">{t("traceDetail.breakTag")}</InlineTag>
                              {event.channelID ? <InlineTag>{event.channelID}</InlineTag> : null}
                              {event.credentialID ? <InlineTag tone="gold">{event.credentialID}</InlineTag> : null}
                            </div>
                            <span className="trace-subline mono">
                              {event.previousRouteTargetID || event.previousUpstreamID || "-"} {"->"} {event.routeTargetID || event.upstreamID || "-"}
                            </span>
                            {event.breakReason ? <span className="trace-subline">{formatFailureReason(event.breakReason)}</span> : null}
                          </div>
                        ))}
                      </div>
                    </section>
                  ) : null}
                  {selectedUpstreamHealth ? (
                    <section className="breakdown-card">
                      <div className="breakdown-title">{t("traceDetail.upstreamHealthAtReview")}</div>
                      <div className="routing-summary-stack">
                        <div className="trace-tag-group">
                          <InlineTag tone={healthTone(selectedUpstreamHealth.health_state)}>{formatHealthLabel(selectedUpstreamHealth.health_state)}</InlineTag>
                          <InlineTag tone={metricThresholdTone(resolveThresholdState(selectedUpstreamHealth.error_rate, selectedUpstreamHealth.health_thresholds?.error_rate_degraded, selectedUpstreamHealth.health_thresholds?.error_rate_open))}>
                            {t("traceDetail.errorLabel")} {resolveThresholdState(selectedUpstreamHealth.error_rate, selectedUpstreamHealth.health_thresholds?.error_rate_degraded, selectedUpstreamHealth.health_thresholds?.error_rate_open)}
                          </InlineTag>
                          <InlineTag tone={metricThresholdTone(resolveThresholdState(selectedUpstreamHealth.timeout_rate, selectedUpstreamHealth.health_thresholds?.timeout_rate_degraded, selectedUpstreamHealth.health_thresholds?.timeout_rate_open))}>
                            {t("traceDetail.timeoutLabel")} {resolveThresholdState(selectedUpstreamHealth.timeout_rate, selectedUpstreamHealth.health_thresholds?.timeout_rate_degraded, selectedUpstreamHealth.health_thresholds?.timeout_rate_open)}
                          </InlineTag>
                        </div>
                        <span className="trace-subline">{buildTraceUpstreamHealthSummary(selectedUpstreamHealth)}</span>
                        <div className="detail-meta-strip">
                          <DetailMetaPill label={t("traceDetail.errorLabel")} value={formatRatio(selectedUpstreamHealth.error_rate)} />
                          <DetailMetaPill label={t("traceDetail.timeoutLabel")} value={formatRatio(selectedUpstreamHealth.timeout_rate)} />
                          <DetailMetaPill label="ttft" value={formatDuration(selectedUpstreamHealth.ttft_fast_ms || 0)} />
                          <DetailMetaPill label={t("traceDetail.latencyLabel")} value={formatDuration(selectedUpstreamHealth.latency_fast_ms || 0)} />
                        </div>
                      </div>
                    </section>
                  ) : null}
                </div>
              ) : null}
              <RoutingDecisionPanel decision={routingDecision} InlineTag={InlineTag} CodeBlock={CodeBlock} showCandidates={!routePlan} t={t} />
            </section>
          ) : null}
          {upstreamCalls.length ? <RelatedUpstreamCallsPanel calls={upstreamCalls} currentTraceID={traceID} fromSessionID={fromSessionID || session?.session_id || ""} t={t} /> : null}
          <section className="panel">
            <div className="panel-head">
              <div>
                <p className="eyebrow">{hasConversation(detail.data) ? t("traceDetail.conversation") : t("traceDetail.payload")}</p>
                <h2>{hasConversation(detail.data) ? t("traceDetail.requestAndResponse") : t("traceDetail.requestResponseBody")}</h2>
              </div>
              <label className="wrap-toggle">
                <input type="checkbox" checked={renderMarkdown} onChange={(event) => setRenderMarkdown(event.target.checked)} />
                {t("traceDetail.renderMarkdown")}
              </label>
            </div>
            {hasConversation(detail.data) ? (
              <div className="message-list">
                {detail.data.messages.map((message, index) => (
                  <MessageCard
                    key={`${message.role}-${index}`}
                    message={message}
                    renderMarkdown={renderMarkdown}
                    declaredTools={declaredTools}
                    CollapsibleCard={CollapsibleCard}
                    CodeBlock={CodeBlock}
                    InlineTag={InlineTag}
                    MessageContent={MessageContent}
                    t={t}
                  />
                ))}
                {detail.data.ai_reasoning ? (
                  <CollapsibleCard title={t("traceDetail.reasoning")} subtitle={t("traceDetail.assistantReasoning")} defaultOpen={false}>
                    <CodeBlock value={detail.data.ai_reasoning} />
                  </CollapsibleCard>
                ) : null}
                {detail.data.ai_content ? (
                  <article className="message-card message-assistant">
                    <div className="message-meta">
                      <span className="role-pill">assistant</span>
                      <span className="message-kind">{t("traceDetail.finalOutput")}</span>
                    </div>
                    <MessageContent value={detail.data.ai_content} format="markdown" renderMarkdown={renderMarkdown} className="message-body" />
                  </article>
                ) : null}
                {detail.data.tool_calls?.length ? (
                  <CollapsibleCard title={t("traceDetail.toolCalls")} subtitle={t("traceDetail.callCount", { count: detail.data.tool_calls.length })} defaultOpen={false}>
                    {detail.data.tool_calls.map((call) => (
                      <ToolCallView key={call.id || call.function?.name} call={call} match={findDeclaredToolForCall(call, declaredTools)} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
                    ))}
                  </CollapsibleCard>
                ) : null}
                {detail.data.ai_blocks?.length ? (
                  <CollapsibleCard title={t("traceDetail.outputBlocks")} subtitle={t("traceDetail.blockCount", { count: detail.data.ai_blocks.length })} defaultOpen={false}>
                    {detail.data.ai_blocks.map((block, index) => (
                      <BlockView key={`${block.kind}-${index}`} block={block} CodeBlock={CodeBlock} />
                    ))}
                  </CollapsibleCard>
                ) : null}
              </div>
            ) : (
              <PayloadSummary raw={raw} CodeBlock={CodeBlock} t={t} />
            )}
          </section>
          <TimelinePanel events={detail.data.events || []} focusTarget={focusTarget} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
          {hasDeclaredToolsTab ? <DeclaredToolsPanel tools={declaredTools} toolCalls={traceToolCalls} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} /> : null}
        </div>
      ) : null}

      {tab === "protocol" ? (
        <ProtocolPanel
          observation={observation}
          CodeBlock={CodeBlock}
          InlineTag={InlineTag}
          busy={jobBusy === "reanalyze"}
          onRefresh={() => runTraceActionWith("reanalyze", apiPaths.traceReanalyze(traceID), { mode: "sync" })}
          t={t}
        />
      ) : null}
      {tab === "audit" ? <AuditPanel findings={findings} InlineTag={InlineTag} CodeBlock={CodeBlock} t={t} /> : null}
      {tab === "performance" ? <PerformancePanel performance={performance} t={t} /> : null}
      {tab === "raw" ? <RawProtocolPanel raw={raw} focusTarget={focusTarget} t={t} /> : null}
    </div>
  );
}

function RelatedUpstreamCallsPanel({ calls = [], currentTraceID = "", fromSessionID = "", t }) {
  return (
    <section className="panel related-upstream-panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.relatedUpstreamCalls")}</p>
          <h2>{t(calls.length === 1 ? "traceDetail.childCallsOne" : "traceDetail.childCalls", { count: calls.length })}</h2>
        </div>
        <InlineTag tone="gold">{t("traceDetail.lineage")}</InlineTag>
      </div>
      <div className="related-upstream-list">
        {calls.map((call, index) => {
          const traceID = call.trace_id || call.id || "";
          const statusCode = Number(call.status_code || 0);
          const failed = statusCode >= 400 || Boolean(call.error_text);
          return (
            <article key={traceID || `${call.exchange_kind || "call"}-${index}`} className="related-upstream-card">
              <div className="related-upstream-main">
                <div>
                  <strong className="trace-model-name">{call.model || t("traceDetail.unknownModel")}</strong>
                  <span className="trace-subline mono">{traceID || call.cassette_path || "-"}</span>
                </div>
                <div className="trace-tag-group">
                  <InlineTag tone={failed ? "danger" : "green"}>{statusCode || (failed ? t("traceDetail.errorLabel") : t("traceDetail.statusOk"))}</InlineTag>
                  <InlineTag tone={call.exchange_kind === "model" ? "gold" : "default"}>{exchangeLabel(call.exchange_role || call.exchange_kind || "model", t)}</InlineTag>
                  <InlineTag tone="accent">{formatEndpointTag(call.endpoint || call.operation)}</InlineTag>
                  <InlineTag>{formatProviderTag(call.provider)}</InlineTag>
                  {call.selected_upstream_id || call.upstream_id || call.route_target ? <InlineTag tone="green">{call.selected_upstream_id || call.upstream_id || call.route_target}</InlineTag> : null}
                </div>
              </div>
              <div className="detail-meta-strip related-upstream-meta">
                <DetailMetaPill label={t("traceDetail.metaSequence")} value={formatSequence(call.sequence_index)} />
                <DetailMetaPill label={t("sessions.duration")} value={formatDuration(call.duration_ms || 0, { precise: true })} />
                <DetailMetaPill label="ttft" value={formatDuration(call.ttft_ms || 0, { precise: true })} />
                {call.response_id ? <DetailMetaPill label={t("traceDetail.metaResponse")} value={call.response_id} mono /> : null}
                {call.request_audit_id ? <DetailMetaPill label={t("traceDetail.metaAudit")} value={call.request_audit_id} mono /> : null}
                {call.parent_exchange_id ? <DetailMetaPill label={t("traceDetail.metaParent")} value={call.parent_exchange_id} mono /> : null}
              </div>
              {call.error_text ? <pre className="timeline-message responses-audit-error">{call.error_text}</pre> : null}
              <div className="related-upstream-actions">
                {traceID && traceID !== currentTraceID ? (
                  <Link className="ghost-button active" to={buildTraceLink(traceID, "requests", fromSessionID, "", failed ? "failure" : "")}>
                    {t("traceDetail.openChildTrace")}
                  </Link>
                ) : null}
                {traceID ? (
                  <Link className="ghost-button" to={buildTraceLink(traceID, "requests", fromSessionID, "raw", failed ? "response" : "")}>
                    {t("requests.raw")}
                  </Link>
                ) : null}
              </div>
            </article>
          );
        })}
      </div>
    </section>
  );
}

function DeclaredToolsPanel({ tools, toolCalls = [], CodeBlock, InlineTag, t }) {
  const [selectedToolName, setSelectedToolName] = useState(() => tools[0]?.name || "");
  const [schemaToolName, setSchemaToolName] = useState("");

  useEffect(() => {
    if (!tools.length) {
      setSelectedToolName("");
      return;
    }
    if (tools.some((tool) => tool.name === selectedToolName)) {
      return;
    }
    setSelectedToolName(tools[0].name || "");
  }, [selectedToolName, tools]);

  const selectedTool = tools.find((tool) => tool.name === selectedToolName) || tools[0] || null;
  const selectedToolCalls = selectedTool ? toolCalls.filter((call) => isSameToolName(call.function?.name, selectedTool.name)) : [];
  const schemaTool = tools.find((tool) => tool.name === schemaToolName) || null;

  return (
    <>
      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("traceDetail.declaredTools")}</p>
            <h2>{t("traceDetail.requestTools")}</h2>
          </div>
        </div>
        {tools.length ? (
          <div className="tool-layout">
            <div className="tool-list-column">
              {tools.map((tool, index) => {
                const count = countToolMatches(toolCalls, tool.name);
                const isSelected = selectedTool?.name === tool.name;
                return (
                  <button
                    key={`${tool.name || "tool"}-${index}`}
                    className={isSelected ? "tool-list-item tool-list-item-active" : "tool-list-item"}
                    onClick={() => {
                      setSelectedToolName(tool.name);
                      setSchemaToolName(tool.name);
                    }}
                  >
                    <div className="tool-list-item-head">
                      <strong>{tool.name || t("traceDetail.toolFallback", { index: index + 1 })}</strong>
                      <InlineTag tone={count > 0 ? "green" : "default"}>{count > 0 ? t(count > 1 ? "traceDetail.toolCallCount" : "traceDetail.toolCallCountOne", { count }) : t("traceDetail.notInvoked")}</InlineTag>
                    </div>
                    <div className="tool-list-item-meta">
                      <span>{tool.source || tool.type || t("traceDetail.toolLabel")}</span>
                      <span>{tool.description || t("traceDetail.toolClickHint")}</span>
                    </div>
                  </button>
                );
              })}
            </div>
            <div className="tool-detail-column">
              {selectedTool ? (
                <>
                  <div className="tool-detail-header">
                    <div>
                      <p className="eyebrow">{t("traceDetail.toolOverview")}</p>
                      <h3>{selectedTool.name}</h3>
                    </div>
                    <div className="trace-tag-group">
                      <InlineTag tone="accent">{selectedTool.source || selectedTool.type || t("traceDetail.toolLabel")}</InlineTag>
                      <InlineTag tone={selectedToolCalls.length ? "green" : "default"}>
                        {selectedToolCalls.length ? t(selectedToolCalls.length > 1 ? "traceDetail.matchedCalls" : "traceDetail.matchedCallsOne", { count: selectedToolCalls.length }) : t("traceDetail.unused")}
                      </InlineTag>
                    </div>
                  </div>
                  <p className="tool-description">{selectedTool.description || t("traceDetail.noDescription")}</p>
                  <div className="tool-detail-actions">
                    <button className="ghost-button" onClick={() => setSchemaToolName(selectedTool.name)}>
                      {t("traceDetail.viewDefinition")}
                    </button>
                  </div>
                  {selectedToolCalls.length ? (
                    <section className="breakdown-card">
                      <div className="breakdown-title">{t("traceDetail.callArguments")}</div>
                      {selectedToolCalls.map((call, index) => (
                        <ToolCallView key={`${call.id || call.function?.name}-${index}`} call={call} match={selectedTool} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
                      ))}
                    </section>
                  ) : (
                    <EmptyState title={t("traceDetail.toolNotInvoked")} detail={t("traceDetail.toolNotInvokedDetail")} compact />
                  )}
                </>
              ) : null}
            </div>
          </div>
        ) : (
          <EmptyState title={t("traceDetail.noDeclaredTools")} detail={t("traceDetail.noDeclaredToolsDetail")} />
        )}
      </section>
      {schemaTool ? (
        <Dialog open onOpenChange={(next) => (next ? undefined : setSchemaToolName(""))}>
          {/* Radix generates the aria-labelledby pair from DialogTitle, so the
              hand-written aria-label and its interpolation key are gone. */}
          <DialogContent className="tool-modal" aria-describedby={undefined}>
            <DialogHeader className="tool-modal-head">
              <div>
                <p className="eyebrow">{t("traceDetail.toolDefinition")}</p>
                <DialogTitle>{schemaTool.name}</DialogTitle>
              </div>
              <DialogClose asChild>
                <button className="icon-button" aria-label={t("traceDetail.closeToolDefinition")}>
                  <span className="tool-modal-close">x</span>
                </button>
              </DialogClose>
            </DialogHeader>
            <div className="trace-tag-group">
              <InlineTag tone="accent">{schemaTool.source || schemaTool.type || t("traceDetail.toolLabel")}</InlineTag>
              <InlineTag>{buildToolSchemaSummary(schemaTool.parameters)}</InlineTag>
            </div>
            {schemaTool.description ? <p className="tool-description">{schemaTool.description}</p> : null}
            <CodeBlock value={schemaTool.parameters || "{}"} />
          </DialogContent>
        </Dialog>
      ) : null}
    </>
  );
}

function ProtocolPanel({ observation, CodeBlock, InlineTag, busy = false, onRefresh, t }) {
  if (observation.error) {
    return (
      <section className="panel protocol-panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("traceDetail.observationIR")}</p>
            <h2>{t("audit.protocol")}</h2>
          </div>
          <button className="ghost-button active" type="button" disabled={busy} onClick={onRefresh}>
            {busy ? t("traceDetail.refreshing") : t("traceDetail.refreshAnalysis")}
          </button>
        </div>
        <EmptyState title={t("traceDetail.protocolUnavailable")} detail={observation.error} tone="danger" compact />
      </section>
    );
  }
  if (observation.loading && !observation.data) {
    return <EmptyState title={t("traceDetail.loadingProtocol")} detail={t("traceDetail.loadingProtocolDetail")} />;
  }
  const summary = observation.data?.summary;
  const tree = observation.data?.tree || [];
  if (!observation.data) {
    return (
      <section className="panel protocol-panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{t("traceDetail.observationIR")}</p>
            <h2>{t("audit.protocol")}</h2>
          </div>
          <button className="ghost-button active" type="button" disabled={busy} onClick={onRefresh}>
            {busy ? t("traceDetail.refreshing") : t("traceDetail.refreshAnalysis")}
          </button>
        </div>
        <EmptyState title={t("traceDetail.noProtocolObservation")} detail={t("traceDetail.noProtocolObservationDetail")} compact />
      </section>
    );
  }
  return (
    <section className="panel protocol-panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.observationIR")}</p>
          <h2>{t("audit.protocol")}</h2>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone={summary?.status === "parsed" ? "green" : "gold"}>{summary?.status || t("traceDetail.unknownStatus")}</InlineTag>
          <InlineTag>{summary?.parser || t("traceDetail.parser")}</InlineTag>
          <InlineTag>{summary?.provider || t("providers.providerFallback")}</InlineTag>
        </div>
        <button className="ghost-button" type="button" disabled={busy} onClick={onRefresh}>
          {busy ? t("traceDetail.refreshing") : t("traceDetail.refreshAnalysis")}
        </button>
      </div>
      <div className="detail-meta-strip">
        <DetailMetaPill label={t("traceDetail.metaModel")} value={summary?.model || "-"} />
        <DetailMetaPill label={t("traceDetail.metaOperation")} value={summary?.operation || "-"} />
        <DetailMetaPill label={t("traceDetail.parser")} value={`${summary?.parser || "-"} ${summary?.parser_version || ""}`.trim()} />
      </div>
      {summary?.warnings ? (
        <CollapsibleCard title={t("traceDetail.parserWarnings")} subtitle={t("traceDetail.tolerantParseNotes")} defaultOpen={false}>
          <CodeBlock value={JSON.stringify(summary.warnings, null, 2)} />
        </CollapsibleCard>
      ) : null}
      {tree.length ? (
        <div className="semantic-tree">
          {tree.map((node) => (
            <SemanticNodeView key={node.id} node={node} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
          ))}
        </div>
      ) : (
        <EmptyState title={t("traceDetail.noSemanticNodes")} detail={t("traceDetail.noSemanticNodesDetail")} compact />
      )}
    </section>
  );
}

function SemanticNodeView({ node, CodeBlock, InlineTag, t }) {
  const hasChildren = Boolean(node.children?.length);
  const raw = node.raw ? JSON.stringify(node.raw, null, 2) : "";
  const body = (
    <>
      {node.text_preview ? <p className="semantic-node-preview">{node.text_preview}</p> : null}
      <div className="detail-meta-strip semantic-node-meta">
        <DetailMetaPill label={t("traceDetail.metaPath")} value={node.path || "-"} mono />
        <DetailMetaPill label={t("traceDetail.metaIndex")} value={node.index ?? 0} />
      </div>
      {raw ? (
        <CollapsibleCard title={t("traceDetail.rawNode")} subtitle={node.path || node.id} defaultOpen={false}>
          <CodeBlock value={raw} />
        </CollapsibleCard>
      ) : null}
      {hasChildren ? (
        <div className="semantic-children">
          {node.children.map((child) => (
            <SemanticNodeView key={child.id} node={child} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
          ))}
        </div>
      ) : null}
    </>
  );
  return (
    <article className="semantic-node">
      <div className="semantic-node-head">
        <div>
          <strong>{node.normalized_type || node.provider_type || t("traceDetail.nodeFallback")}</strong>
          <span className="mono">{node.id}</span>
        </div>
        <div className="trace-tag-group">
          <InlineTag tone="accent">{node.provider_type || t("providers.providerFallback")}</InlineTag>
          {node.role ? <InlineTag>{node.role}</InlineTag> : null}
        </div>
      </div>
      {body}
    </article>
  );
}

function AuditPanel({ findings, InlineTag, CodeBlock, t }) {
  if (findings.error) {
    return <EmptyState title={t("audit.loadFindingsError")} detail={findings.error} tone="danger" />;
  }
  if (findings.loading && !findings.data) {
    return <EmptyState title={t("audit.loadingFindings")} detail={t("traceDetail.loadingFindingsDetail")} />;
  }
  const items = Array.isArray(findings.data?.items) ? findings.data.items : Array.isArray(findings.data) ? findings.data : [];
  return (
    <section className="panel audit-panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.deterministicAudit")}</p>
          <h2>{t("overview.findings")}</h2>
        </div>
        <InlineTag tone={items.length ? "danger" : "green"}>{t(items.length === 1 ? "traceDetail.findingCountOne" : "traceDetail.findingCount", { count: items.length })}</InlineTag>
      </div>
      {items.length ? (
        <div className="finding-list">
          {items.map((finding) => (
            <article key={finding.id} className="finding-card">
              <div className="finding-card-head">
                <div>
                  <strong>{finding.title || finding.category}</strong>
                  <span>{finding.description || finding.category}</span>
                </div>
                <div className="trace-tag-group">
                  <InlineTag tone={finding.severity === "high" || finding.severity === "critical" ? "danger" : "gold"}>{finding.severity}</InlineTag>
                  <InlineTag>{finding.category}</InlineTag>
                </div>
              </div>
              <div className="detail-meta-strip">
                <DetailMetaPill label={t("traceDetail.metaDetector")} value={`${finding.detector || "-"} ${finding.detector_version || ""}`.trim()} />
                <DetailMetaPill label={t("traceDetail.metaConfidence")} value={Number(finding.confidence || 0).toFixed(2)} />
                <DetailMetaPill label={t("traceDetail.metaNode")} value={finding.node_id || "-"} mono />
                <DetailMetaPill label={t("traceDetail.metaEvidence")} value={finding.evidence_path || "-"} mono />
              </div>
              {finding.evidence_excerpt ? <CodeBlock value={finding.evidence_excerpt} /> : null}
            </article>
          ))}
        </div>
      ) : (
        <EmptyState title={t("audit.noFindings")} detail={t("traceDetail.noFindingsDetail")} compact />
      )}
    </section>
  );
}

function buildResponsesAuditLink(trace) {
  if (!trace) {
    return "";
  }
  const responseID = firstAuditIdentifier(trace, "response_id");
  const requestAuditID = firstAuditIdentifier(trace, "request_audit_id");
  const params = new URLSearchParams();
  if (responseID) {
    params.set("response_id", responseID);
  } else if (requestAuditID) {
    params.set("request_audit_id", requestAuditID);
  }
  const query = params.toString();
  return query ? `/audit?${query}` : "";
}

function firstAuditIdentifier(trace, key) {
  const sources = [
    trace,
    trace.header?.meta,
    trace.request_audit,
    trace.responses_audit,
    trace.audit,
    ...(Array.isArray(trace.upstream_exchanges) ? trace.upstream_exchanges : []),
    ...(Array.isArray(trace.events) ? trace.events : []),
  ];
  for (const source of sources) {
    const value = typeof source?.[key] === "string" ? source[key].trim() : "";
    if (value) {
      return value;
    }
  }
  return "";
}

function formatSequence(value) {
  if (value === null || value === undefined || value === "") {
    return "-";
  }
  return `#${value}`;
}

function exchangeLabel(value = "", t) {
  switch (String(value || "").trim()) {
    case "primary_model_call":
      return t("traceDetail.exchangeModel");
    case "client_request":
      return t("traceDetail.exchangeRequest");
    case "upstream_model_call":
    case "model_call":
      return t("traceDetail.exchangeModel");
    case "model":
      return t("traceDetail.exchangeModel");
    case "entry":
      return t("traceDetail.exchangeRequest");
    default:
      return value || t("traceDetail.exchangeModel");
  }
}

function PerformancePanel({ performance, t }) {
  if (performance.error) {
    return <EmptyState title={t("traceDetail.loadPerformanceError")} detail={performance.error} tone="danger" />;
  }
  if (performance.loading && !performance.data) {
    return <EmptyState title={t("traceDetail.loadingPerformance")} detail={t("traceDetail.loadingPerformanceDetail")} />;
  }
  const perf = performance.data?.performance;
  if (!perf) {
    return <EmptyState title={t("traceDetail.noPerformanceData")} detail={t("traceDetail.noPerformanceDataDetail")} />;
  }
  return (
    <section className="panel performance-panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.runtimeMetrics")}</p>
          <h2>{t("traceDetail.performanceTitle")}</h2>
        </div>
      </div>
      <section className="hero-grid">
        <StatCard label={t("traceDetail.duration")} value={formatDuration(perf.duration_ms || 0, { precise: true })} />
        <StatCard label="TTFT" value={formatDuration(perf.ttft_ms || 0, { precise: true })} />
        <StatCard label={t("traceDetail.tokensPerSec")} value={Number(perf.tokens_per_sec || 0).toFixed(2)} accent="accent-green" />
        <StatCard label={t("traceDetail.cache")} value={`${Number(perf.cache_ratio || 0).toFixed(1)}%`} accent="accent-gold" />
      </section>
      <div className="detail-meta-strip">
        <DetailMetaPill label={t("traceDetail.metaStatus")} value={perf.status_code || 0} />
        <DetailMetaPill label={t("traceDetail.totalTokens")} value={formatTokenCount(perf.total_tokens || 0)} />
        <DetailMetaPill label={t("traceDetail.input")} value={formatTokenCount(perf.prompt_tokens || 0)} />
        <DetailMetaPill label={t("traceDetail.output")} value={formatTokenCount(perf.completion_tokens || 0)} />
        <DetailMetaPill label={t("traceDetail.cached")} value={formatTokenCount(perf.cached_tokens || 0)} />
        <DetailMetaPill label={t("routing.stream")} value={perf.is_stream ? t("traceDetail.yes") : t("traceDetail.no")} />
        <DetailMetaPill label={t("traceDetail.metaUpstream")} value={perf.selected_upstream_id || "-"} mono />
        <DetailMetaPill label={t("traceDetail.metaPolicy")} value={perf.routing_policy || "-"} />
      </div>
      {perf.provider_error ? <pre className="trace-failure-detail">{perf.provider_error}</pre> : null}
    </section>
  );
}

function RawProtocolPanel({ raw, focusTarget = "", t }) {
  const [wrap, setWrap] = useState(false);
  const requestRef = useRef(null);
  const responseRef = useRef(null);

  useEffect(() => {
    if (focusTarget === "request" && requestRef.current) {
      requestRef.current.scrollIntoView({ block: "start", behavior: "smooth" });
      return;
    }
    if (focusTarget === "response" && responseRef.current) {
      responseRef.current.scrollIntoView({ block: "start", behavior: "smooth" });
    }
  }, [focusTarget]);

  if (raw.error) {
    return <EmptyState title={t("traceDetail.loadRawError")} detail={raw.error} tone="danger" />;
  }
  if (raw.loading && !raw.data) {
    return <EmptyState title={t("traceDetail.loadingRaw")} detail={t("traceDetail.loadingRawDetail")} />;
  }

  return (
    <section className="panel raw-panel">
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.rawHttpExchange")}</p>
          <h2>{t("traceDetail.requestResponse")}</h2>
        </div>
        <label className="wrap-toggle">
          <input type="checkbox" checked={wrap} onChange={(event) => setWrap(event.target.checked)} />
          {t("traceDetail.wrapLines")}
        </label>
      </div>
      <div className="raw-grid">
        <ProtocolColumn ref={requestRef} title={t("traceDetail.request")} value={raw.data?.request_protocol || ""} wrap={wrap} focused={focusTarget === "request"} />
        <ProtocolColumn ref={responseRef} title={t("traceDetail.response")} value={raw.data?.response_protocol || ""} wrap={wrap} focused={focusTarget === "response"} />
      </div>
    </section>
  );
}

function TimelinePanel({ events, focusTarget = "", CodeBlock, InlineTag, t }) {
  const panelRef = useRef(null);
  const focusPath = focusTarget === "timeline_error" ? findFirstTimelineErrorPath(events) : [];

  useEffect(() => {
    if ((focusTarget !== "timeline" && focusTarget !== "timeline_error") || !panelRef.current) {
      return;
    }
    panelRef.current.scrollIntoView({ block: "start", behavior: "smooth" });
  }, [focusTarget]);

  if (!events.length) {
    return <EmptyState title={t("traceDetail.noTimelineEvents")} detail={t("traceDetail.noTimelineEventsDetail")} />;
  }

  return (
    <section ref={panelRef} className={focusTarget === "timeline" ? "panel timeline-panel timeline-panel-focused" : "panel timeline-panel"}>
      <div className="panel-head">
        <div>
          <p className="eyebrow">{t("traceDetail.providerTimeline")}</p>
          <h2>{t("traceDetail.unifiedEventStream")}</h2>
        </div>
      </div>
      <div className="timeline-list">
        {events.map((event, index) => (
          <article key={`${event.type}-${index}`} className="timeline-item">
            <div className="timeline-rail">
              <span className={event.type?.startsWith("llm.") ? "timeline-dot timeline-dot-live" : "timeline-dot"} />
            </div>
            <div className="timeline-card">
              <div className="timeline-head">
                <div>
                  <strong>{event.type || t("traceDetail.eventFallback")}</strong>
                  <span>{formatDateTime(event.time)}</span>
                </div>
                <span className="timeline-badge">{event.is_stream ? t("routing.stream") : t("traceDetail.recordTag")}</span>
              </div>
              {event.timeline_items?.length ? <TimelineTree items={event.timeline_items} focusPath={focusPath} InlineTag={InlineTag} t={t} /> : null}
              {!event.timeline_items?.length && event.message ? <div className="timeline-message">{event.message}</div> : null}
              {event.attributes ? <CodeBlock value={JSON.stringify(event.attributes, null, 2)} /> : null}
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function RoutePlanSummary({ plan, selectedUpstreamBaseURL = "", selectedUpstreamProviderPreset = "", InlineTag, t }) {
  const selectedID = plan.selectedRouteTargetID || plan.selectedUpstreamID || "";
  const modelAliased = Boolean(plan.requestedModel && plan.upstreamModel && plan.requestedModel !== plan.upstreamModel);
  return (
    <div className="routing-summary-grid">
      <section className="breakdown-card">
        <div className="breakdown-title">{selectedID ? t("traceDetail.resolvedRouteTarget") : t("traceDetail.failureClass")}</div>
        <div className="routing-summary-stack">
          <strong className="trace-model-name">{selectedID || formatFailureReason(plan.failureReason) || t("traceDetail.routingFailure")}</strong>
          <span className="trace-subline mono">{plan.upstreamEndpoint || selectedUpstreamBaseURL || "-"}</span>
          <div className="trace-tag-group">
            {selectedUpstreamProviderPreset ? <InlineTag tone="accent">{selectedUpstreamProviderPreset}</InlineTag> : null}
            {plan.selectedChannelID ? <InlineTag>{plan.selectedChannelID}</InlineTag> : null}
            {plan.selectedUpstreamID ? <InlineTag tone="green">{compactUpstreamID(plan.selectedUpstreamID)}</InlineTag> : null}
            {plan.strategy ? <InlineTag>{formatRoutePlanValue(plan.strategy)}</InlineTag> : null}
          </div>
        </div>
      </section>
      <section className="breakdown-card">
        <div className="breakdown-title">{t("traceDetail.modelMapping")}</div>
        <div className="routing-summary-stack">
          <div className="detail-meta-strip">
            <DetailMetaPill label={t("traceDetail.metaRequested")} value={plan.requestedModel || "-"} mono />
            <DetailMetaPill label={t("traceDetail.metaUpstream")} value={plan.upstreamModel || "-"} mono />
          </div>
          {modelAliased ? (
            <div className="trace-tag-group">
              <InlineTag tone="gold">{t("traceDetail.aliasRewrite")}</InlineTag>
              <span className="trace-subline mono">{plan.requestedModel} {"->"} {plan.upstreamModel}</span>
            </div>
          ) : (
            <span className="trace-subline">{t("traceDetail.modelNamesMatch")}</span>
          )}
        </div>
      </section>
      <section className="breakdown-card">
        <div className="breakdown-title">{t("routing.execution")}</div>
        <div className="routing-summary-stack">
          <div className="detail-meta-strip">
            <DetailMetaPill label={t("traceDetail.metaEntrypoint")} value={plan.clientEntrypoint || "-"} />
            <DetailMetaPill label={t("traceDetail.metaMode")} value={formatRoutePlanValue(plan.executionMode)} />
            <DetailMetaPill label={t("traceDetail.metaEndpoint")} value={plan.upstreamEndpoint || "-"} />
            <DetailMetaPill label={t("traceDetail.metaStrategy")} value={formatRoutePlanValue(plan.strategy)} />
          </div>
        </div>
      </section>
      {plan.failureReason ? (
        <section className="breakdown-card">
          <div className="breakdown-title">{t("traceDetail.failureReason")}</div>
          <div className="routing-summary-stack">
            <strong>{formatFailureReason(plan.failureReason)}</strong>
            {plan.reason && plan.reason !== plan.failureReason ? <span className="trace-subline">{formatFailureReason(plan.reason)}</span> : null}
          </div>
        </section>
      ) : null}
      {plan.candidateSummary.length ? (
        <section className="breakdown-card route-plan-candidates">
          <div className="breakdown-title">{t("traceDetail.candidateSummary")}</div>
          <div className="routing-candidate-list routing-candidate-list-compact">
            {plan.candidateSummary.map((candidate, index) => (
              <article key={`${candidate.route_target_id || candidate.id || "candidate"}-${index}`} className={candidate.selectable ? "routing-candidate-card routing-candidate-card-active" : "routing-candidate-card"}>
                <div className="routing-candidate-head">
                  <div>
                    <strong>{candidate.route_target_id || candidate.id || t("routing.unknownTarget")}</strong>
                    {candidate.channel_id ? <span className="trace-subline mono">{candidate.channel_id}</span> : null}
                  </div>
                  <div className="trace-tag-group">
                    {candidate.api_type ? <InlineTag tone="accent">{candidate.api_type}</InlineTag> : null}
                    {candidate.mode ? <InlineTag>{candidate.mode}</InlineTag> : null}
                    <InlineTag tone={candidate.selectable ? "green" : "gold"}>{candidate.selectable ? t("routing.selectable") : candidate.filter_reason || t("routing.filtered")}</InlineTag>
                  </div>
                </div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("traceDetail.metaPath")} value={candidate.supports_path ? t("traceDetail.yes") : t("traceDetail.no")} />
                  <DetailMetaPill label={t("traceDetail.metaModel")} value={candidate.supports_model ? t("traceDetail.yes") : t("traceDetail.no")} />
                </div>
              </article>
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}

function RoutingDecisionPanel({ decision, InlineTag, CodeBlock, showCandidates = true, t }) {
  if (!decision || (!decision.candidates.length && !decision.events.length)) {
    return null;
  }
  const selectedID = decision.selectedRouteTargetID || decision.selectedID || "";
  return (
    <section className="routing-decision-panel">
      <div className="routing-decision-head">
        <div>
          <div className="breakdown-title">{t("traceDetail.decisionTrace")}</div>
          <strong>{selectedID ? t("traceDetail.selectedTarget", { target: selectedID }) : decision.failureReason ? formatFailureReason(decision.failureReason) : t("traceDetail.routingEvents")}</strong>
        </div>
        <div className="trace-tag-group">
          {decision.policy ? <InlineTag>{decision.policy}</InlineTag> : null}
          {decision.fallbackPolicy ? <InlineTag>{decision.fallbackPolicy}</InlineTag> : null}
          {decision.selectedChannelID ? <InlineTag tone="accent">{decision.selectedChannelID}</InlineTag> : null}
          {decision.selectedCredentialID ? <InlineTag tone="gold">{decision.selectedCredentialID}</InlineTag> : null}
          {decision.outcome?.attributes?.status_code ? <InlineTag tone={Number(decision.outcome.attributes.status_code) >= 400 ? "danger" : "green"}>{decision.outcome.attributes.status_code}</InlineTag> : null}
        </div>
      </div>
      {showCandidates && decision.candidates.length ? (
        <div className="routing-candidate-list">
          {decision.candidates.map((candidate, index) => (
            <article key={`${candidate.id || "candidate"}-${index}`} className={candidate.selectable ? "routing-candidate-card routing-candidate-card-active" : "routing-candidate-card"}>
              <div className="routing-candidate-head">
                <div>
                  <strong>{candidate.route_target_id || candidate.id || t("routing.unknownTarget")}</strong>
                  <span className="trace-subline mono">{candidate.base_url || "-"}</span>
                </div>
                <div className="trace-tag-group">
                  {candidate.provider_preset ? <InlineTag tone="accent">{candidate.provider_preset}</InlineTag> : null}
                  {candidate.channel_id ? <InlineTag>{candidate.channel_id}</InlineTag> : null}
                  {candidate.credential_id ? <InlineTag tone="gold">{candidate.credential_id}</InlineTag> : null}
                  <InlineTag tone={candidate.selectable ? "green" : "gold"}>{candidate.selectable ? t("routing.selectable") : candidate.filter_reason || t("routing.filtered")}</InlineTag>
                  {candidate.health_state ? <InlineTag tone={healthTone(candidate.health_state)}>{formatHealthLabel(candidate.health_state)}</InlineTag> : null}
                </div>
              </div>
              <div className="detail-meta-strip">
                <DetailMetaPill label={t("traceDetail.metaPriority")} value={candidate.priority ?? "-"} />
                <DetailMetaPill label={t("traceDetail.metaWeight")} value={candidate.weight ?? "-"} />
                <DetailMetaPill label={t("traceDetail.metaPath")} value={candidate.supports_path ? t("traceDetail.yes") : t("traceDetail.no")} />
                <DetailMetaPill label={t("traceDetail.metaModel")} value={candidate.supports_model ? t("traceDetail.yes") : t("traceDetail.no")} />
                {candidate.credential_hint ? <DetailMetaPill label={t("traceDetail.metaHint")} value={candidate.credential_hint} mono /> : null}
              </div>
            </article>
          ))}
        </div>
      ) : showCandidates ? (
        <EmptyState title={t("traceDetail.noCandidateDetail")} detail={t("traceDetail.noCandidateDetailText")} compact />
      ) : null}
      {decision.events.length ? (
        <CollapsibleCard title={t("traceDetail.routingEventPayloads")} subtitle={t("traceDetail.eventCount", { count: decision.events.length })} defaultOpen={false}>
          <CodeBlock value={JSON.stringify(decision.events, null, 2)} />
        </CollapsibleCard>
      ) : null}
    </section>
  );
}

function TimelineTree({ items, focusPath = [], InlineTag, t }) {
  return (
    <div className="timeline-tree">
      {items.map((item, index) => (
        <TimelineNode key={buildTimelineNodeKey(item, index)} nodeKey={buildTimelineNodeKey(item, index)} item={item} depth={0} focusPath={focusPath} InlineTag={InlineTag} t={t} />
      ))}
    </div>
  );
}

function TimelineNode({ item, depth = 0, nodeKey = "", focusPath = [], InlineTag, t }) {
  const nodeRef = useRef(null);
  const hasChildren = Boolean(item.children?.length);
  const hasDetails = Boolean(item.body && item.body !== item.summary);
  const collapsible = hasChildren || hasDetails;
  const focused = focusPath.includes(nodeKey);
  const focusedBranch = focusPath.length > 0 && focused;
  const className = `timeline-node timeline-node-${item.kind || "item"}${focused ? " timeline-node-focused" : ""}`;

  useEffect(() => {
    if (!focused || !nodeRef.current) {
      return;
    }
    nodeRef.current.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focused]);

  if (!collapsible) {
    return (
      <div ref={nodeRef} className={className}>
        <div className="timeline-node-leaf">
          <TimelineNodeHeading item={item} t={t} />
          {item.id ? <span className="timeline-node-id">{item.id}</span> : null}
          {item.status === "error" ? <InlineTag tone="danger">{t("traceDetail.errorLabel")}</InlineTag> : null}
        </div>
        {item.summary ? <div className="timeline-node-preview">{item.summary}</div> : null}
      </div>
    );
  }

  return (
    <details ref={nodeRef} className={className} open={(depth === 0 && hasChildren) || focusedBranch}>
      <summary className="timeline-node-summary">
        <TimelineNodeHeading item={item} t={t} />
        {item.id ? <span className="timeline-node-id">{item.id}</span> : null}
        {item.status === "error" ? <InlineTag tone="danger">{t("traceDetail.errorLabel")}</InlineTag> : null}
      </summary>
      {item.summary ? <div className="timeline-node-preview">{item.summary}</div> : null}
      {hasDetails ? <pre className="timeline-node-body">{item.body}</pre> : null}
      {hasChildren ? (
        <div className="timeline-children">
          {item.children.map((child, index) => (
            <TimelineNode key={buildTimelineNodeKey(child, index)} nodeKey={buildTimelineNodeKey(child, index)} item={child} depth={depth + 1} focusPath={focusPath} InlineTag={InlineTag} t={t} />
          ))}
        </div>
      ) : null}
    </details>
  );
}

function TimelineNodeHeading({ item, t }) {
  return (
    <div className="timeline-node-heading">
      <span className="timeline-node-kind">{formatTimelineKind(item.kind, t)}</span>
      <strong className="timeline-node-title">{formatTimelineTitle(item, t)}</strong>
    </div>
  );
}

function buildRoutingDecision(events = []) {
  const routingEvents = events.filter((event) => event.type?.startsWith("routing."));
  if (!routingEvents.length) {
    return { events: [], candidates: [], stickyBreaks: [] };
  }
  const classified = routingEvents.find((event) => event.type === "routing.classified")?.attributes || {};
  const candidatesEvent = routingEvents.find((event) => event.type === "routing.candidates")?.attributes || {};
  const selected = routingEvents.find((event) => event.type === "routing.selected")?.attributes || {};
  const filtered = routingEvents.find((event) => event.type === "routing.filtered")?.attributes || {};
  const outcome = [...routingEvents].reverse().find((event) => event.type === "routing.outcome") || null;
  const stickyBreaks = routingEvents
    .filter((event) => event.type === "routing.sticky.break")
    .map((event) => normalizeStickyBreak(event.attributes || {}));
  const selectedRouteTargetID = selected.route_target_id || outcome?.attributes?.route_target_id || selected.upstream_id || outcome?.attributes?.upstream_id || "";
  const selectedChannelID = selected.channel_id || outcome?.attributes?.channel_id || selected.upstream_id || outcome?.attributes?.upstream_id || "";
  const selectedCredentialID = selected.credential_id || outcome?.attributes?.credential_id || "";
  const selectedCredentialHint = selected.credential_hint || outcome?.attributes?.credential_hint || "";
  return {
    events: routingEvents,
    model: classified.model || outcome?.attributes?.model || "",
    endpoint: classified.endpoint || "",
    policy: classified.routing_policy || "",
    fallbackPolicy: classified.fallback_policy || "",
    selectedID: selected.upstream_id || outcome?.attributes?.upstream_id || "",
    selectedRouteTargetID,
    selectedChannelID,
    selectedCredentialID,
    selectedCredentialHint,
    failureReason: filtered.routing_failure_reason || "",
    availableCount: Number(candidatesEvent.available_count || 0),
    candidates: Array.isArray(candidatesEvent.candidates) ? candidatesEvent.candidates : [],
    stickyBreaks,
    outcome,
  };
}

function buildRoutePlan(events = []) {
  const routePlanEvent = [...events].reverse().find((event) => event.type === "routing.route_plan");
  const attrs = routePlanEvent?.attributes || null;
  if (!attrs) {
    return null;
  }
  return {
    event: routePlanEvent,
    clientEntrypoint: attrs.client_entrypoint || attrs.entrypoint || "",
    executionMode: attrs.execution_mode || "",
    strategy: attrs.strategy || attrs.routing_policy || "",
    requestedModel: attrs.requested_model || "",
    upstreamModel: attrs.upstream_model || "",
    selectedRouteTargetID: attrs.selected_route_target_id || attrs.route_target_id || "",
    selectedChannelID: attrs.selected_channel_id || attrs.channel_id || "",
    selectedUpstreamID: attrs.selected_upstream_id || attrs.upstream_id || "",
    upstreamEndpoint: attrs.upstream_endpoint || attrs.endpoint || "",
    failureReason: attrs.failure_reason || attrs.routing_failure_reason || "",
    reason: attrs.reason || "",
    candidateSummary: Array.isArray(attrs.candidate_summary) ? attrs.candidate_summary : [],
  };
}

function formatRoutePlanValue(value = "") {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  return text.replaceAll("_", " ");
}

function normalizeStickyBreak(attrs = {}) {
  return {
    routeTargetID: attrs.route_target_id || attrs.upstream_id || "",
    upstreamID: attrs.upstream_id || attrs.target_id || "",
    channelID: attrs.channel_id || "",
    credentialID: attrs.credential_id || "",
    credentialHint: attrs.credential_hint || "",
    previousRouteTargetID: attrs.previous_route_target_id || attrs.previous_upstream_id || "",
    previousUpstreamID: attrs.previous_upstream_id || attrs.break_id || "",
    breakReason: attrs.break_reason || attrs.reason || attrs.routing_failure_reason || "",
  };
}

function PayloadSummary({ raw, CodeBlock, t }) {
  const requestBody = extractHTTPBody(raw.data?.request_protocol || "");
  const responseBody = extractHTTPBody(raw.data?.response_protocol || "");

  return (
    <div className="payload-grid">
      <section className="payload-card">
        <div className="protocol-head">{t("traceDetail.requestBody")}</div>
        <CodeBlock value={formatBodyForDisplay(requestBody, t)} />
      </section>
      <section className="payload-card">
        <div className="protocol-head">{t("traceDetail.responseBody")}</div>
        <CodeBlock value={formatBodyForDisplay(responseBody, t)} />
      </section>
    </div>
  );
}

const ProtocolColumn = React.forwardRef(function ProtocolColumn({ title, value, wrap, focused = false }, ref) {
  return (
    <div ref={ref} className={focused ? "protocol-column protocol-column-focused" : "protocol-column"}>
      <div className="protocol-head">{title}</div>
      <pre className={wrap ? "protocol-code protocol-code-wrap" : "protocol-code"}>{value}</pre>
    </div>
  );
});

function MessageCard({ message, renderMarkdown, declaredTools = [], CollapsibleCard, CodeBlock, InlineTag, MessageContent, t }) {
  const alignClass = message.role === "assistant" ? "message-assistant" : message.role === "tool" ? "message-tool" : "message-user";
  const isCollapsible = message.message_type === "tool_use" || message.message_type === "tool_result";
  const toolSummary = buildToolMessageSummary(message, declaredTools);
  const callID = message.tool_call_id || "";
  const outputAnchor = callID ? toolOutputAnchor(callID) : "";
  const cardID = outputAnchor || undefined;

  const body = (
    <article id={cardID} className={`message-card ${alignClass}`}>
      <div className="message-meta">
        <span className="role-pill">{message.role}</span>
        <span className="message-kind">{message.message_type || "message"}</span>
        {callID ? (
          <>
            <span className="message-call-id">{t("traceDetail.callID", { id: callID })}</span>
            <a className="message-jump-link" href={`#${toolCallAnchor(callID)}`}>{t("traceDetail.callLink")}</a>
          </>
        ) : null}
      </div>
      {toolSummary ? <div className="tool-message-summary">{toolSummary}</div> : null}
      {message.content ? (
        <MessageContent value={message.content} format={message.content_format} renderMarkdown={renderMarkdown} className="message-body" />
      ) : null}
      {message.tool_calls?.length ? message.tool_calls.map((call) => (
        <ToolCallView key={call.id || call.function?.name} call={call} match={findDeclaredToolForCall(call, declaredTools)} CodeBlock={CodeBlock} InlineTag={InlineTag} t={t} />
      )) : null}
      {message.blocks?.length ? message.blocks.map((block, index) => <BlockView key={`${block.kind}-${index}`} block={block} CodeBlock={CodeBlock} />) : null}
      {!message.content && !message.tool_calls?.length && !message.blocks?.length ? (
        <div className="tool-message-placeholder">{t("traceDetail.noStructuredPayload")}</div>
      ) : null}
    </article>
  );

  if (!isCollapsible) {
    return body;
  }

  return (
    <CollapsibleCard title={`${message.role} / ${message.message_type}`} subtitle={toolSummary || message.name || message.tool_call_id || ""} defaultOpen={false} bodyClassName="collapse-plain">
      {body}
    </CollapsibleCard>
  );
}

function ToolCallView({ call, match = null, CodeBlock, InlineTag, t }) {
  const callID = call.id || "";
  return (
    <div id={callID ? toolCallAnchor(callID) : undefined} className="tool-call-box">
      <div className="tool-call-head">
        <div className="tool-call-title">{call.function?.name || t("traceDetail.toolLabel")}</div>
        {match?.name ? <InlineTag tone="accent">{t("traceDetail.declared")}</InlineTag> : null}
      </div>
      {callID ? (
        <div className="tool-call-meta">
          {t("traceDetail.callID", { id: callID })}
          <a className="message-jump-link" href={`#${toolOutputAnchor(callID)}`}>{t("traceDetail.output")}</a>
        </div>
      ) : null}
      <CodeBlock value={call.function?.arguments || "{}"} />
    </div>
  );
}

function toolCallAnchor(callID = "") {
  return `call-${anchorToken(callID)}`;
}

function toolOutputAnchor(callID = "") {
  return `output-${anchorToken(callID)}`;
}

function anchorToken(value = "") {
  return encodeURIComponent(String(value || "").trim()).replaceAll("%", "_");
}

function BlockView({ block, CodeBlock }) {
  return (
    <div className="tool-call-box">
      <div className="tool-call-title">{block.title || block.kind}</div>
      <CodeBlock value={block.text || block.meta || ""} />
    </div>
  );
}

function extractHTTPBody(value = "") {
  if (!value) {
    return "";
  }
  const separator = value.includes("\r\n\r\n") ? "\r\n\r\n" : "\n\n";
  const index = value.indexOf(separator);
  if (index === -1) {
    return value;
  }
  return value.slice(index + separator.length);
}

function formatBodyForDisplay(value = "", t) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return t("traceDetail.emptyBody");
  }
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return trimmed;
  }
}

function formatTimelineKind(kind = "", t) {
  switch (kind) {
    case "message":
      return t("traceDetail.kindMessage");
    case "tool_call":
      return t("traceDetail.kindToolCall");
    case "tool_response":
      return t("traceDetail.kindToolResponse");
    case "thinking":
      return t("traceDetail.kindThinking");
    case "output":
      return t("traceDetail.kindOutput");
    default:
      return kind || t("traceDetail.kindItem");
  }
}

function formatTimelineTitle(item = {}, t) {
  if (item.kind === "message") {
    return item.label || item.role || t("traceDetail.messageTitle");
  }
  return item.name || item.label || formatTimelineKind(item.kind, t);
}

function buildTimelineNodeKey(item = {}, index = 0) {
  return `${item.kind || "item"}-${item.id || item.name || item.label || index}`;
}

function findFirstTimelineErrorPath(events = []) {
  for (const event of events) {
    const path = findTimelineItemErrorPath(event.timeline_items || []);
    if (path.length) {
      return path;
    }
  }
  return [];
}

function findTimelineItemErrorPath(items = []) {
  for (let index = 0; index < items.length; index += 1) {
    const item = items[index];
    const nodeKey = buildTimelineNodeKey(item, index);
    if (item.status === "error") {
      return [nodeKey];
    }
    const childPath = findTimelineItemErrorPath(item.children || []);
    if (childPath.length) {
      return [nodeKey, ...childPath];
    }
  }
  return [];
}

function hasConversation(detail) {
  return Boolean(
    detail?.messages?.length ||
      detail?.ai_content ||
      detail?.ai_reasoning ||
      detail?.ai_blocks?.length ||
      detail?.tool_calls?.length
  );
}

function labelTraceAction(action, t) {
  switch (action) {
    case "repair":
      return t("traceDetail.statsRepair");
    case "reanalyze":
      return t("traceDetail.analysisRefresh");
    default:
      return t("nav.analysis");
  }
}

function compactUpstreamID(value = "") {
  if (/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(String(value))) {
    return "upstream";
  }
  return String(value || "upstream").length > 18 ? `${String(value).slice(0, 10)}...` : String(value || "upstream");
}
