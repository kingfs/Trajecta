import React from "react";
import { TabbedPage } from "../components/TabbedPage";
import { ConnectPanel } from "./access/ConnectPanel";
import { TokensPanel } from "./access/TokensPanel";
import { useI18n } from "../lib/i18n";

// 接入 groups the two halves of "let a client talk to the proxy": the protocol
// entrypoint cheat sheet, and the tokens that authenticate the client.
export function AccessPage() {
  const { t } = useI18n();
  return (
    <TabbedPage
      eyebrow={t("nav.group.configure")}
      title={t("nav.access")}
      subtitle={t("access.subtitle")}
      tabs={[
        { id: "clients", label: t("access.tabClients"), element: <ConnectPanel /> },
        { id: "tokens", label: t("access.tabTokens"), element: <TokensPanel /> },
      ]}
    />
  );
}
