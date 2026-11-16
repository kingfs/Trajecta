import React, { useState } from "react";
import { ChevronRight, FlaskConical } from "lucide-react";
import { Card } from "../../components/ui/card";
import { Input } from "../../components/ui/input";
import { Button } from "../../components/ui/button";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { DetectorPluginDrawer } from "../../components/monitor/DetectorPluginDrawer";
import { DetectorPluginSwitch } from "../../components/monitor/DetectorPluginSwitch";
import { DetectorPluginConflictDialog } from "../../components/monitor/DetectorPluginConflictDialog";
import { DetectorStatsSection } from "../../components/monitor/DetectorStatsSection";
import { DetectorSandboxDialog } from "../../components/monitor/DetectorSandboxDialog";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, patchJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useDetectorPluginToggle } from "../../hooks/useDetectorPluginToggle";
import { useWriteMutation } from "../../lib/mutations";
import {
  localizedName,
  localizedDescription,
  filterPlugins,
  orderedDetectorLanes,
  detectorPIISettings,
} from "../../lib/plugins";

// DetectorPluginsPanel presents the detection-algorithm catalog: name,
// description, kind/contract/lane tags and the enable switch. Clicking a card
// opens the detail drawer (rules, secretmask's PII setting). It mirrors the
// channel panel: the server owns the disable check, and its 409 payload —
// here the stand-in mapping count — drives the force confirmation.
//
// Two observability surfaces sit alongside the catalog: the collapsible stats
// section (hot-path counters, 30-day per-rule replacements, suppression and
// mapping sizes) and the read-only secretmask rule sandbox dialog.
export function DetectorPluginsPanel() {
  const { t, language } = useI18n();
  const plugins = useJSON(apiPaths.detectorPlugins, []);
  const [detailID, setDetailID] = useState("");
  const [query, setQuery] = useState("");
  const [sandboxOpen, setSandboxOpen] = useState(false);
  const { busyID, conflict, setConflict, enable, requestDisable, onToggle } = useDetectorPluginToggle();

  // The settings write replaces the plugin's opaque settings object, so the
  // caller merges over the current value (detectorPIISettings) to keep the
  // keys it does not own, such as disabled_rules.
  const saveSettings = useWriteMutation({
    mutationFn: ({ item, settings }) => patchJSON(apiPaths.detectorPlugin(item.id), { settings }),
    success: "plugins.detectionPiiSaved",
    invalidate: [["GET", apiPaths.detectorPlugins]],
  });

  const items = plugins.data?.items || [];
  const capabilities = plugins.data?.capabilities || [];
  const visibleItems = filterPlugins(items, query);
  // Resolve the open drawer against the freshest data so a toggle made inside
  // it is reflected without closing it.
  const detailItem = detailID ? (items.find((item) => item.id === detailID) || null) : null;

  if (plugins.error) {
    return <EmptyState title={t("plugins.loadError")} detail={plugins.error} tone="danger" />;
  }
  if (plugins.loading && !plugins.data) {
    return <EmptyState title={t("plugins.loading")} detail={t("plugins.detectionLoadingDetail")} />;
  }

  return (
    <>
      <div className="plugins-toolbar">
        <Input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder={t("plugins.searchPlaceholder")}
          aria-label={t("plugins.searchPlaceholder")}
          className="plugins-search"
        />
        <Button variant="ghost" type="button" onClick={() => setSandboxOpen(true)}>
          <FlaskConical size={13} aria-hidden="true" />
          <span>{t("plugins.sandbox.button")}</span>
        </Button>
      </div>
      {capabilities.length ? (
        <Card as="section" className="detector-capabilities">
          <div className="panel-head">
            <div>
              <h2>{t("plugins.detectionCapabilities")}</h2>
              <p className="system-note">{t("plugins.detectionCapabilitiesHint")}</p>
            </div>
          </div>
          <div className="detector-capability-grid">
            {capabilities.map((cap) => (
              <div className="detector-capability" key={cap.id}>
                <div className="detector-capability-head">
                  <strong>{language === "en" ? cap.name : cap.name_zh || cap.name}</strong>
                  {cap.report_only ? <InlineTag tone="gold">{t("plugins.detectionReportOnly")}</InlineTag> : null}
                </div>
                <p className="trace-subline">{language === "en" ? cap.description : cap.description_zh || cap.description}</p>
                <span className="detector-capability-count">{t("plugins.detectionRulesCount", { count: (cap.rules || []).length })}</span>
              </div>
            ))}
          </div>
        </Card>
      ) : null}
      <DetectorStatsSection />
      <div className="provider-grid">
        {visibleItems.map((item) => (
          <Card
            key={item.id}
            className="plugin-card"
            role="button"
            tabIndex={0}
            aria-label={t("plugins.viewDetails", { name: localizedName(item, language) })}
            onClick={() => setDetailID(item.id)}
            onKeyDown={(event) => {
              if (event.target !== event.currentTarget) return;
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                setDetailID(item.id);
              }
            }}
          >
            <div className="plugin-card-head">
              <div className="plugin-card-name">
                <h2>{localizedName(item, language)}</h2>
                {item.kind ? <InlineTag tone="accent">{t(`plugins.kind.${item.kind}`)}</InlineTag> : null}
              </div>
              <DetectorPluginSwitch
                item={item}
                disabled={busyID === item.id || enable.isPending}
                onChange={(next) => onToggle(item, next)}
              />
            </div>
            <p className="plugin-card-desc">{localizedDescription(item, language)}</p>
            <div className="plugin-card-foot">
              <div className="detector-plugin-tags" onClick={(event) => event.stopPropagation()}>
                {item.contract ? <InlineTag tone="default">{t(`plugins.contract.${item.contract}`)}</InlineTag> : null}
                {orderedDetectorLanes(item.lanes).map((lane) => (
                  <InlineTag key={lane} tone={lane === "hot" ? "gold" : "default"}>{t(`plugins.lane.${lane}`)}</InlineTag>
                ))}
              </div>
              <span className="plugin-card-more" aria-hidden="true">
                {t("plugins.details")}
                <ChevronRight size={13} />
              </span>
            </div>
          </Card>
        ))}
        {!items.length ? <EmptyState title={t("plugins.detectionNone")} detail={t("plugins.detectionNoneDetail")} /> : null}
        {items.length && !visibleItems.length ? <EmptyState title={t("plugins.searchEmpty")} detail={t("plugins.searchEmptyDetail")} /> : null}
      </div>

      <DetectorPluginDrawer
        item={detailItem}
        open={Boolean(detailItem)}
        onClose={() => { if (conflict) setConflict(null); else setDetailID(""); }}
        enabled={Boolean(detailItem?.enabled)}
        onToggle={detailItem ? (next) => onToggle(detailItem, next) : undefined}
        toggleDisabled={busyID === detailID || enable.isPending}
        onPIIToggle={detailItem ? (next) => saveSettings.mutate({ item: detailItem, settings: detectorPIISettings(detailItem, next) }) : undefined}
        piiToggleDisabled={busyID === detailID || saveSettings.isPending}
      />

      <DetectorPluginConflictDialog
        conflict={conflict}
        busy={conflict ? busyID === conflict.plugin.id : false}
        onCancel={() => setConflict(null)}
        onConfirm={() => requestDisable(conflict.plugin, true)}
      />

      <DetectorSandboxDialog open={sandboxOpen} onOpenChange={setSandboxOpen} />
    </>
  );
}
