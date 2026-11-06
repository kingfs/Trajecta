import React from "react";
import { CollapsibleCard } from "../common/Display";
import { useI18n } from "../../lib/i18n";
import { RatesAlertsPanel, RouteInspectorPanel, RoutingSettingsPanel } from "../../routes/RoutingPage";

/*
 * The mount point for the deployment-wide routing controls that used to be a
 * standalone /routing page.
 *
 * They are advanced material - a deployment-wide policy and a dry run - so they
 * sit at the bottom of the channel page inside collapsed disclosures rather
 * than in the flow of the channel list. The disclosure is the shared
 * CollapsibleCard: Radix gives the trigger `aria-expanded` and a content id, and
 * the content is unmounted while closed, so the settings read and the inspector
 * only mount when a reader opens the card. The card titles and subtitles reuse
 * existing routing.* dictionary entries, so this move added no strings.
 *
 * The outer grid exists because CollapsibleCard is a bare surface: unlike the
 * card primitive it carries no bottom margin, and two stacked disclosures would
 * otherwise touch.
 */
export function ChannelRoutingPanels() {
  const { t } = useI18n();
  return (
    <div className="grid gap-4">
      <CollapsibleCard title={t("routing.settings")} subtitle={t("routing.systemPolicy")} defaultOpen={false}>
        <RoutingSettingsPanel />
      </CollapsibleCard>
      <CollapsibleCard title={t("routing.ratesAlerts")} subtitle={t("routing.ratesAlertsDetail")} defaultOpen={false}>
        <RatesAlertsPanel />
      </CollapsibleCard>
      <CollapsibleCard title={t("routing.routeInspector")} subtitle={t("routing.dryRun")} defaultOpen={false}>
        <RouteInspectorPanel />
      </CollapsibleCard>
    </div>
  );
}
