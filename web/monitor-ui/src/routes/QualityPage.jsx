import React from "react";
import { useSearchParams } from "react-router-dom";
import { TabbedPage } from "../components/TabbedPage";
import { WindowToggle } from "../components/common/Tabs";
import { FindingsPanel } from "./quality/FindingsPanel";
import { LineagePanel } from "./quality/LineagePanel";
import { AnalysisPanel } from "./quality/AnalysisPanel";
import { DataHealthPanel } from "./quality/DataHealthPanel";
import { normalizeAnalyticsWindow, setOrDeleteParam } from "../lib/monitor";
import { useI18n } from "../lib/i18n";

// The audit page used to be three unrelated things stacked in one scroll: a
// deterministic findings list, a Responses-lineage lookup tool, and a
// server-side tool configuration form. The first two are what "quality" means
// here; the configuration form moved to 系统 → 服务端工具.
export function QualityPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const setWindow = (nextWindow) => {
    const next = new URLSearchParams(searchParams);
    setOrDeleteParam(next, "window", nextWindow === "today" ? "" : nextWindow);
    setSearchParams(next);
  };
  // Deep links that carry a lineage selector have to land on the lineage tab,
  // otherwise opening a finding from the trace detail view would silently show
  // the findings list instead of the trace it linked to.
  const lineageRequested = searchParams.has("response_id") || searchParams.has("request_audit_id");

  return (
    <TabbedPage
      title={t("nav.quality")}
      defaultTab={lineageRequested ? "lineage" : "findings"}
      tabs={[
        { id: "findings", label: t("quality.tabFindings"), element: <FindingsPanel /> },
        { id: "lineage", label: t("quality.tabLineage"), element: <LineagePanel /> },
        { id: "analysis", label: t("quality.tabAnalysis"), element: <AnalysisPanel /> },
        {
          id: "health",
          label: t("quality.tabHealth"),
          element: <DataHealthPanel />,
          // The health tab is the one audit view that reads an analytics window,
          // so it is the one that gets the range control in the page header.
          actions: <WindowToggle value={windowValue} onChange={setWindow} label={t("overview.window")} />,
        },
      ]}
    />
  );
}
