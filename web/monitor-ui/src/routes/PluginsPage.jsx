import React from "react";
import { TabbedPage } from "../components/TabbedPage";
import { useI18n } from "../lib/i18n";
import { ChannelPluginsPanel } from "./plugins/ChannelPluginsPanel";
import { DetectorPluginsPanel } from "./plugins/DetectorPluginsPanel";

// Plugins is the home of the catalog switches: channel plugins gate the
// official vendor catalog, detection plugins gate the detection algorithms.
// The tab strip leaves room for future plugin kinds without adding navigation
// entries.
export function PluginsPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      title={t("plugins.title")}
      tabs={[
        { id: "channels", label: t("plugins.tabChannels"), element: <ChannelPluginsPanel /> },
        { id: "detection", label: t("plugins.tabDetection"), element: <DetectorPluginsPanel /> },
      ]}
    />
  );
}