import React from "react";
import { Card } from "../ui/card";
import { Button } from "../ui/button";
import { EmptyState } from "../common/EmptyState";
import { InlineTag } from "../common/Badges";
import { ResourceTree } from "./ResourceTree";

/*
 * The per-trace resources as directory trees: the local paths and URLs the
 * exchange carried, folded by the directories and hosts they share. No file
 * content is stored, and a value appears once however many times the trace
 * repeated it; the number on a row is the count of semantic steps in its
 * subtree.
 *
 * The refresh action runs a reparse, which is what fills the index for traces
 * recorded before the index existed. It is an explicit button rather than a
 * write on page load: opening a trace should not change the database.
 */
export function TraceResources({ data, loading, error, onRefresh, refreshing, onLocate, t }) {
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
        <ResourceTree items={items} countKey="access_count" t={t} onLocate={onLocate} />
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
