import React from "react";
import { TabbedPage } from "../components/TabbedPage";
import { RuntimePanel } from "./system/RuntimePanel";
import { DatabasePanel } from "./system/DatabasePanel";
import { SlowQueryPanel } from "./system/SlowQueryPanel";
import { ToolsPanel } from "./system/ToolsPanel";
import { useI18n } from "../lib/i18n";

// The system page is admin-only and reads three independent snapshots - host and
// process runtime, PostgreSQL statistics, slow statements - plus the server-side
// tool bindings. Each is its own request and its own tab, so a slow statement
// collector that was never armed cannot hold up the database view, and the
// database view cannot hold up the process view.
export function SystemPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      title={t("system.title")}
      tabs={[
        { id: "runtime", label: t("system.tabRuntime"), element: <RuntimePanel /> },
        { id: "database", label: t("system.tabDatabase"), element: <DatabasePanel /> },
        { id: "slow", label: t("system.tabSlowQueries"), element: <SlowQueryPanel /> },
        { id: "tools", label: t("system.tabTools"), element: <ToolsPanel /> },
      ]}
    />
  );
}
