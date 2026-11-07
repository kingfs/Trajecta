import React from "react";
import { useSearchParams } from "react-router-dom";
import { TabbedPage } from "../components/TabbedPage";
import { WindowToggle } from "../components/common/Tabs";
import { useI18n } from "../lib/i18n";
import { normalizeAnalyticsWindow, setOrDeleteParam } from "../lib/monitor";
import { ModelSquarePanel } from "./models/ModelSquarePanel";
import { ModelsUsagePanel } from "./models/ModelsUsagePanel";

/**
 * The model page is two questions on one address.
 *
 * 模型广场 answers "what can this installation route right now" and is the
 * default, because that is the question an operator has when they arrive from
 * the rail. 实时统计 answers "what is being used" over a time window and is one
 * tab away; it keeps the analytics list that used to own the whole page.
 *
 * The window picker belongs to the statistics tab only. It is supplied through
 * the tab's `actions` slot so the page header never shows a range control over
 * a catalogue that has no time dimension.
 */
export function ModelsPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      title={t("nav.models")}
      defaultTab="square"
      tabs={[
        { id: "square", label: t("models.tabSquare"), element: <ModelSquarePanel /> },
        { id: "usage", label: t("models.tabUsage"), element: <ModelsUsagePanel />, actions: <UsageWindowControl /> },
      ]}
    />
  );
}

function UsageWindowControl() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    next.delete("page");
    setSearchParams(next);
  };
  return <WindowToggle value={windowValue} onChange={setWindow} label={t("models.window")} />;
}
