import React from "react";
import { ExternalLink, FileText, LifeBuoy, Tag } from "lucide-react";
import { GitHubMark } from "../components/common/GitHubMark";
import { Badge } from "../components/ui/badge";
import { Card } from "../components/ui/card";
import { EmptyState } from "../components/common/EmptyState";
import { PageHeader } from "../components/common/PageHeader";
import { useJSON } from "../hooks/useJSON";
import { apiPaths } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { formatDateTime } from "../lib/monitor";
import { ReleaseCheckCard } from "./about/ReleaseCheckCard";
import { UsageStatsCard } from "./about/UsageStatsCard";
import { SystemFact } from "./system/format";

/*
 * 关于: which build is running, where the project lives, and whether there is
 * something newer.
 *
 * It is deliberately not a tab of 系统. That page answers "is the machine
 * healthy", is admin-only, and reads process internals, database statistics and
 * statement text; this one answers "which build is this" and is the one page
 * every signed-in role may open. Merging them would have forced the build facts
 * behind the admin gate, or the process internals out of it.
 *
 * The version itself is not read from the frontend bundle: it is injected into
 * the Go binary at link time and read back from /api/about, so the number the
 * page prints is the number the server is running even when a stale asset
 * bundle is cached somewhere.
 */
export function AboutPage() {
  const { t } = useI18n();
  const { loading, data, error } = useJSON(apiPaths.about);

  const version = data?.version || "";
  const commits = Number(data?.commits_since_tag || 0);

  return (
    <div className="shell shell-list">
      <PageHeader
        title={t("about.title")}
        actions={
          data?.repository ? (
            <a className="about-header-link" href={data.repository} target="_blank" rel="noreferrer">
              <GitHubMark size={16} />
              <span>{t("about.openRepository")}</span>
              <ExternalLink size={13} aria-hidden="true" />
            </a>
          ) : null
        }
      />

      {error ? <EmptyState tone="danger" title={t("about.loadFailed")} detail={error} /> : null}
      {!error && loading && !data ? <p className="system-note">{t("system.loading")}</p> : null}

      {data ? (
        <>
          <Card as="section" className="about-hero-card">
            <div className="about-hero">
              <span className="about-mark" aria-hidden="true">T</span>
              <div className="about-hero-copy">
                <h2>{data.name || "Trajecta"}</h2>
                <p className="about-tagline">{t("about.tagline")}</p>
                <div className="about-badges">
                  <Badge tone="brand" className="mono">{version || t("about.versionUnknown")}</Badge>
                  {data.commit ? <Badge tone="muted" className="mono">{data.commit}</Badge> : null}
                  {data.branch ? <Badge tone="muted">{data.branch}</Badge> : null}
                  {commits > 0 ? <Badge tone="info">{t("about.commitsSinceTag", { count: commits })}</Badge> : null}
                </div>
              </div>
            </div>

            <div className="system-kv about-facts">
              <SystemFact label={t("about.version")} value={version || t("about.versionUnknown")} />
              <SystemFact label={t("about.baseVersion")} value={data.base_version} />
              <SystemFact label={t("about.commit")} value={data.commit} />
              <SystemFact label={t("about.buildDate")} value={data.build_date ? formatDateTime(data.build_date) : ""} />
              <SystemFact label={t("about.branch")} value={data.branch} />
              <SystemFact label={t("about.goVersion")} value={data.go_version} />
              <SystemFact label={t("about.platform")} value={`${data.goos || ""}/${data.goarch || ""}`} />
              <SystemFact label={t("about.license")} value={data.license} />
            </div>

            {/* Every link the project publishes, in one row. They are the reason
                the page exists as much as the version is: an operator who wants
                to report a bug or read the upgrade steps should not have to
                search for the repository. */}
            <div className="about-links">
              <AboutLink href={data.repository} icon={<GitHubMark size={15} />} label={t("about.repository")} />
              <AboutLink href={data.releases_url} icon={<Tag size={15} aria-hidden="true" />} label={t("about.releases")} />
              <AboutLink href={data.issues_url} icon={<LifeBuoy size={15} aria-hidden="true" />} label={t("about.issues")} />
              <AboutLink href={data.docs_url} icon={<FileText size={15} aria-hidden="true" />} label={t("about.docs")} />
            </div>
          </Card>

          <ReleaseCheckCard about={data} />
          <UsageStatsCard about={data} />
        </>
      ) : null}
    </div>
  );
}

function AboutLink({ href, icon, label }) {
  if (!href) {
    return null;
  }
  return (
    <a className="about-link" href={href} target="_blank" rel="noreferrer">
      {icon}
      <span>{label}</span>
      <ExternalLink size={12} aria-hidden="true" />
    </a>
  );
}
