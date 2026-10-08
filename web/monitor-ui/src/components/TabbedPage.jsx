import React from "react";
import { useSearchParams } from "react-router-dom";
import { PageHeader } from "./common/PageHeader";
import { TabStrip } from "./common/Tabs";
import { useI18n } from "../lib/i18n";

/**
 * TabbedPage turns several sibling views into one page.
 *
 * The active tab lives in the `?tab=` query parameter rather than in component
 * state: the redesigned console merges pages that used to be separate
 * destinations (traffic, quality, access, system), and a merged page whose
 * panels cannot be linked to would be a regression. Other query parameters are
 * preserved, so a filtered list survives a tab switch.
 *
 * Each tab is `{ id, label, element, actions }`; `actions` renders in the page
 * header and is how a panel exposes its own primary action (a repair button, a
 * create button) without owning the header.
 */
export function TabbedPage({ eyebrow, title, subtitle, tabs, defaultTab, meta }) {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const fallback = defaultTab || tabs[0]?.id;
  const requested = searchParams.get("tab");
  const activeId = tabs.some((tab) => tab.id === requested) ? requested : fallback;
  const active = tabs.find((tab) => tab.id === activeId) || tabs[0];

  const selectTab = (nextId) => {
    const next = new URLSearchParams(searchParams);
    if (nextId === fallback) {
      next.delete("tab");
    } else {
      next.set("tab", nextId);
    }
    setSearchParams(next);
  };

  if (!active) {
    return null;
  }

  return (
    <main className="shell shell-list">
      <PageHeader eyebrow={eyebrow} title={title} subtitle={subtitle} meta={meta} actions={active.actions} />
      <TabStrip tabs={tabs} activeId={activeId} onSelect={selectTab} label={t("common.pageSections")} />
      <div className="page-body" role="tabpanel" id={`tabpanel-${active.id}`} aria-labelledby={`tab-${active.id}`}>
        {active.element}
      </div>
    </main>
  );
}
