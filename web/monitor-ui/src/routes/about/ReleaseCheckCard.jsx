import React, { useState } from "react";
import { RefreshCw, Sparkles } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { Card } from "../../components/ui/card";
import { MessageContent } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { apiPaths, requestJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { SystemFact } from "../system/format";

/*
 * 版本更新: the one place in the console that reaches for the network.
 *
 * It is a button rather than a read-on-mount on purpose. A Monitor is often
 * deployed where nothing may call out, and a page that silently requests
 * api.github.com every time it is opened spends the deployment's rate limit and
 * its operator's trust. Pressing the button is the consent.
 *
 * The result is not an HTTP error even when the lookup failed: the endpoint
 * answers 200 with a status, so every outcome - up to date, newer, unknown,
 * turned off, unreachable - is one shape the page renders. A thrown error here
 * therefore means the Monitor itself did not answer, which is a different
 * sentence and gets a different card.
 */
export function ReleaseCheckCard({ about }) {
  const { t } = useI18n();
  const [state, setState] = useState({ status: "idle", result: null, error: "" });

  const check = async () => {
    setState({ status: "loading", result: null, error: "" });
    try {
      const result = await requestJSON(apiPaths.versionLatest);
      setState({ status: "done", result, error: "" });
    } catch (error) {
      setState({ status: "failed", result: null, error: error?.message || String(error) });
    }
  };

  const result = state.result;

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("about.update.title")}</h2>
          <p className="system-note">{t("about.update.description")}</p>
        </div>
        <div className="panel-head-actions">
          {result ? <ReleaseStatusBadge status={result.status} /> : null}
          {result?.cached ? <Badge tone="muted">{t("about.update.cached")}</Badge> : null}
          {about?.update_check_enabled ? (
            <Button variant="primary" type="button" onClick={check} disabled={state.status === "loading"}>
              <RefreshCw size={14} aria-hidden="true" className={state.status === "loading" ? "animate-spin" : undefined} />
              {state.status === "loading" ? t("about.update.checking") : t("about.update.check")}
            </Button>
          ) : (
            <Badge tone="muted">{t("about.update.disabledBadge")}</Badge>
          )}
        </div>
      </div>

      {!about?.update_check_enabled ? (
        <EmptyState title={t("about.update.disabled")} detail={t("about.update.disabledDetail")} />
      ) : null}

      {state.status === "failed" ? (
        <EmptyState
          tone="danger"
          title={t("about.update.monitorFailed")}
          detail={state.error}
          action={
            <Button variant="default" type="button" onClick={check}>
              {t("about.update.retry")}
            </Button>
          }
        />
      ) : null}

      {state.status === "done" && result ? <ReleaseResult result={result} about={about} /> : null}

      {/* Only while the button is actually there: the disabled state above says
          everything the idle sentence would, and saying it twice reads as two
          separate problems. */}
      {state.status === "idle" && about?.update_check_enabled ? (
        <p className="system-note">{t("about.update.idle")}</p>
      ) : null}
    </Card>
  );
}

function ReleaseResult({ result, about }) {
  const { t } = useI18n();
  const newer = result.status === "update_available";
  const unknown = result.status === "unknown";
  const noRelease = result.status === "no_release";
  const unavailable = result.status === "unavailable" || result.status === "disabled";

  return (
    <div className="about-update-result">
      {newer ? (
        <p className="about-update-headline">
          <Sparkles size={16} aria-hidden="true" />
          {t("about.update.available", { version: result.latest_version || "" })}
        </p>
      ) : null}
      {result.status === "up_to_date" ? (
        <p className="about-update-headline about-update-headline-ok">{t("about.update.upToDate")}</p>
      ) : null}
      {unknown ? <p className="about-update-headline">{t("about.update.unknown")}</p> : null}
      {noRelease ? (
        <EmptyState
          title={t("about.update.noRelease")}
          detail={t("about.update.noReleaseDetail")}
          action={
            about?.releases_url ? (
              <a href={about.releases_url} target="_blank" rel="noreferrer">
                {t("about.releases")}
              </a>
            ) : null
          }
        />
      ) : null}
      {unavailable ? (
        <EmptyState
          tone="warning"
          title={result.status === "disabled" ? t("about.update.disabled") : t("about.update.unavailable")}
          detail={result.error || ""}
        />
      ) : null}

      <div className="system-kv">
        <SystemFact label={t("about.update.current")} value={result.current_version} />
        <SystemFact label={t("about.update.latest")} value={result.latest_version} />
        <SystemFact
          label={t("about.update.publishedAt")}
          value={result.published_at ? formatDateTime(result.published_at) : ""}
        />
        <SystemFact label={t("about.update.checkedAt")} value={result.checked_at ? formatDateTime(result.checked_at) : ""} />
      </div>

      {result.status === "up_to_date" && Number(result.commits_since_tag || 0) > 0 ? (
        <p className="system-note">{t("about.update.aheadDetail", { count: result.commits_since_tag })}</p>
      ) : null}
      {unknown && result.error ? <p className="system-note">{result.error}</p> : null}

      {result.release_url ? (
        <p className="about-update-link">
          <a href={result.release_url} target="_blank" rel="noreferrer">
            {t("about.update.openRelease")}
          </a>
          {about?.repository ? (
            <>
              {" · "}
              <a href={about.repository} target="_blank" rel="noreferrer">
                {t("about.repository")}
              </a>
            </>
          ) : null}
        </p>
      ) : null}

      {result.notes ? (
        <div className="about-release-notes">
          <h3>{t("about.update.notes")}</h3>
          <MessageContent value={result.notes} format="markdown" renderMarkdown collapsedLines={14} />
        </div>
      ) : null}
    </div>
  );
}

function ReleaseStatusBadge({ status }) {
  const { t } = useI18n();
  const tones = {
    update_available: "info",
    up_to_date: "success",
    unknown: "warning",
    no_release: "muted",
    unavailable: "warning",
    disabled: "muted",
  };
  return <Badge tone={tones[status] || "muted"}>{t(`about.update.status.${status}`)}</Badge>;
}
