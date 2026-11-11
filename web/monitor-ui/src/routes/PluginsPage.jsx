import React from "react";
import { TabbedPage } from "../components/TabbedPage";
import { useI18n } from "../lib/i18n";
import { ChannelPluginsPanel } from "./plugins/ChannelPluginsPanel";

// Plugins is the home of the catalog switches. It starts with one tab (channel
// plugins); the tab strip leaves room for future plugin kinds without adding
// navigation entries.
export function PluginsPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      title={t("plugins.title")}
      tabs={[
        { id: "channels", label: t("plugins.tabChannels"), element: <ChannelPluginsPanel /> },
      ]}
    />
  );
}