import React from "react";
import { useSearchParams } from "react-router-dom";
import { TabbedPage } from "../components/TabbedPage";
import { FindingsPanel } from "./quality/FindingsPanel";
import { LineagePanel } from "./quality/LineagePanel";
import { AnalysisPanel } from "./quality/AnalysisPanel";
import { DataHealthPanel } from "./quality/DataHealthPanel";
import { SecretMappingsPanel } from "./quality/SecretMappingsPanel";
import { SecretSuppressionsPanel } from "./quality/SecretSuppressionsPanel";
import { useI18n } from "../lib/i18n";

// The audit page used to be three unrelated things stacked in one scroll: a
// deterministic findings list, a Responses-lineage lookup tool, and a
// server-side tool configuration form. Quality now means the findings, the
// global masking map and its false-positive suppression list, the lineage
// lookup, the analysis queue and derived-data health; the configuration form
// moved to 系统 → 服务端工具.
export function QualityPage() {
  const { t } = useI18n();
  const [searchParams] = useSearchParams();
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
        // The mapping table is a global store, not a window of traffic. It sits
        // beside the findings because a false positive is discovered there, but
        // it also lists values whose trace has already been pruned.
        { id: "mappings", label: t("quality.tabMappings"), element: <SecretMappingsPanel /> },
        // Suppressions are the policy half of the same store: a mapping row can
        // be recorded and still be suppressed from future masking.
        { id: "suppressions", label: t("quality.tabSuppressions"), element: <SecretSuppressionsPanel /> },
        { id: "lineage", label: t("quality.tabLineage"), element: <LineagePanel /> },
        { id: "analysis", label: t("quality.tabAnalysis"), element: <AnalysisPanel /> },
        // Data health is pipeline state, not a traffic window, so it carries no
        // range control; see DataHealthPanel.
        { id: "health", label: t("quality.tabHealth"), element: <DataHealthPanel /> },
      ]}
    />
  );
}
