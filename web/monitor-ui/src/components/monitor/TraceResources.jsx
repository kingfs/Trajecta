import React from "react";
import { Card } from "../ui/card";
import { Button } from "../ui/button";
import { EmptyState } from "../common/EmptyState";
import { DetailMetaPill, InlineTag } from "../common/Badges";
import { formatDateTime } from "../../lib/monitor";

/*
 * The per-trace resource list: the local paths and URLs the exchange carried.
 * No file content is stored, and a value is listed once however many times the
 * trace repeated it; the pill reports how many semantic steps carried it.
 *
 * The refresh action runs a reparse, which is what fills the index for traces
 * recorded before the index existed. It is an explicit button rather than a
 * write on page load: opening a trace should not change the database.
 */
export function TraceResources({ data, loading, error, onRefresh, refreshing, t }) {
  const items = Array.isArray(data?.items) ? data.items : [];
  const refreshAction = (
    <Button variant="primary" type="button" disabled={refreshing} onClick={onRefresh}>
      {refreshing ? t("resources.refreshing") : t("resources.refresh")}
    </Button>
  );

  return (
    <Card as="section">
      <div className="panel-head">
        <div>
          <h2>{t("resources.traceTitle")}</h2>
          <p className="trace-subline">{t("resources.traceHint")}</p>
        </div>
        <div className="panel-head-actions">
          <InlineTag>{t("resources.count", { count: items.length })}</InlineTag>
          {refreshAction}
        </div>
      </div>

      {error ? <EmptyState title={t("resources.loadError")} detail={error} tone="danger" /> : null}
      {loading && !data ? <EmptyState title={t("resources.loading")} detail={t("resources.loadingDetail")} /> : null}

      {items.length ? (
        <div className="finding-list">
          {items.map((item) => {
            const isURL = item.kind === "url";
            return (
              <article key={`${item.kind}-${item.id}`} className="finding-card">
                <div className="finding-card-head">
                  <div>
                    <strong className="mono resource-value" title={item.value}>{item.value}</strong>
                    <span>{t("resources.nodeCount")}: {item.access_count}</span>
                  </div>
                  <div className="trace-tag-group">
                    <InlineTag tone={isURL ? "accent" : "green"}>{isURL ? t("resources.kindUrl") : t("resources.kindPath")}</InlineTag>
                  </div>
                </div>
                <div className="detail-meta-strip">
                  <DetailMetaPill label={t("resources.firstSeen")} value={formatDateTime(item.first_seen_at)} />
                  <DetailMetaPill label={t("resources.lastSeen")} value={formatDateTime(item.last_seen_at)} />
                </div>
              </article>
            );
          })}
        </div>
      ) : data && !error ? (
        <EmptyState
          title={t("resources.emptyTitle")}
          detail={t("resources.emptyDetail")}
          action={refreshAction}
        />
      ) : null}
    </Card>
  );
}
