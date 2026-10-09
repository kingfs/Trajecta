import React from "react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Link } from "react-router-dom";
import { EmptyState } from "../../components/common/EmptyState";
import { DetailMetaPill, InlineTag } from "../../components/common/Badges";
import { useJSON } from "../../hooks/useJSON";
import { useRefresh } from "../../hooks/useRefresh";
import { apiPaths, apiURL, postJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { formatDateTime } from "../../lib/monitor";

export function AnalysisPanel() {
  const refresh = useRefresh();
  const { t } = useI18n();
  const analysis = useJSON(apiURL(apiPaths.analysis, { limit: "50" }), []);
  const jobs = useJSON(apiURL(apiPaths.analysisJobs, { limit: "50" }), []);
  const items = analysis.data?.items || [];
  const jobItems = jobs.data?.items || [];

  // The two batches share one busy flag because they disable both buttons while
  // either is in flight; each mutation owns its own pending state now.
  const runMissingUsageBatch = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.analysisBatchReanalyze, { mode: "async", missing_usage: true, limit: 1000, repair_usage: true }),
    success: (response) => t("analysis.queuedJob", { id: response?.job?.id || "-" }),
    error: "sessionDetail.requestFailed",
    onSuccess: () => refresh(),
  });

  const runAnalysisRepairBatch = useWriteMutation({
    mutationFn: () => Promise.all([
      postJSON(apiPaths.analysisBatchReanalyze, { mode: "async", observation: "failed", limit: 1000, reparse: true, scan: true }),
      postJSON(apiPaths.analysisBatchReanalyze, { mode: "async", observation: "unparsed", limit: 1000, reparse: true, scan: true }),
    ]),
    success: (responses) => t("analysis.queuedJobs", { count: responses.length }),
    error: "sessionDetail.requestFailed",
    onSuccess: () => refresh(),
  });
  const batchBusy = runMissingUsageBatch.isPending || runAnalysisRepairBatch.isPending;

  return (
    <>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("analysis.jobQueue")}</h2>
            <p className="trace-subline">{t("analysis.jobsHint")}</p>
          </div>
          <div className="panel-head-actions" role="group" aria-label={t("analysis.batchRepair")}>
            <InlineTag>{t("analysis.jobs", { count: jobs.data?.total ?? 0 })}</InlineTag>
            <Button variant="primary" type="button" disabled={batchBusy} onClick={() => runAnalysisRepairBatch.mutate()}>
              {batchBusy ? t("analysis.queueing") : t("analysis.refreshProblemData")}
            </Button>
            <Button variant="primary" type="button" disabled={batchBusy} onClick={() => runMissingUsageBatch.mutate()}>
              {batchBusy ? t("analysis.queueing") : t("analysis.repairMissingUsage")}
            </Button>
          </div>
        </div>
        {jobs.error ? <EmptyState title={t("analysis.loadJobsError")} detail={jobs.error} tone="danger" /> : null}
        {jobs.loading && !jobs.data ? <EmptyState title={t("analysis.loadingJobs")} detail={t("analysis.loadingJobsDetail")} /> : null}
        {jobItems.length ? (
          <div className="finding-list">
            {jobItems.map((job) => (
              <article key={job.id} className="finding-card">
                <div className="finding-card-head">
                  <div>
                    <strong>{job.job_type}</strong>
                    <span>{job.target_type} / {job.target_id}</span>
                  </div>
                  <InlineTag tone={job.status === "completed" ? "green" : job.status === "failed" ? "danger" : "gold"}>{job.status}</InlineTag>
                </div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("analysis.metaJob")} value={job.id} />
                  <DetailMetaPill label={t("analysis.metaAttempts")} value={job.attempts ?? 0} />
                  <DetailMetaPill label={t("analysis.metaCreated")} value={formatDateTime(job.created_at)} />
                  <DetailMetaPill label={t("analysis.metaUpdated")} value={formatDateTime(job.updated_at)} />
                </div>
                <pre className="code-block">{JSON.stringify({ steps: job.steps || [], request: job.request || {}, result: job.result || {}, error: job.last_error || "" }, null, 2)}</pre>
              </article>
            ))}
          </div>
        ) : jobs.data ? (
          <EmptyState title={t("analysis.noJobs")} detail={t("analysis.noJobsDetail")} />
        ) : null}
      </Card>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("analysis.latest")}</h2>
          </div>
          <InlineTag>{t("analysis.totalRuns", { count: analysis.data?.total ?? 0 })}</InlineTag>
        </div>
        {analysis.error ? <EmptyState title={t("analysis.loadError")} detail={analysis.error} tone="danger" /> : null}
        {analysis.loading && !analysis.data ? <EmptyState title={t("analysis.loading")} detail={t("analysis.loadingDetail")} /> : null}
        {items.length ? (
          <div className="finding-list">
            {items.map((run) => (
              <article key={run.id} className="finding-card">
                <div className="finding-card-head">
                  <div>
                    <strong>{run.kind}</strong>
                    <span>{run.analyzer} {run.analyzer_version}</span>
                  </div>
                  <InlineTag tone={run.status === "completed" ? "green" : "gold"}>{run.status}</InlineTag>
                </div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("analysis.metaSession")} value={run.session_id || "-"} mono />
                  <DetailMetaPill label={t("analysis.metaTrace")} value={run.trace_id || "-"} mono />
                  <DetailMetaPill label={t("analysis.metaInput")} value={run.input_ref || "-"} mono />
                  <DetailMetaPill label={t("analysis.metaCreated")} value={formatDateTime(run.created_at)} />
                </div>
                <div className="action-group action-group-start">
                  {run.session_id ? <Button asChild variant="ghost" to={`/sessions/${encodeURIComponent(run.session_id)}`}><Link to={`/sessions/${encodeURIComponent(run.session_id)}`}>{t("analysis.openSession")}</Link></Button> : null}
                  {run.trace_id ? <Button asChild variant="ghost" to={`/traces/${encodeURIComponent(run.trace_id)}`}><Link to={`/traces/${encodeURIComponent(run.trace_id)}`}>{t("analysis.openTrace")}</Link></Button> : null}
                </div>
              </article>
            ))}
          </div>
        ) : analysis.data ? (
          <EmptyState title={t("analysis.noRuns")} detail={t("analysis.noRunsDetail")} />
        ) : null}
      </Card>
    </>
  );
}
