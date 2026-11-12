import React, { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight } from "lucide-react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { Switch } from "../../components/common/Controls";
import { DetectorPluginDrawer } from "../../components/monitor/DetectorPluginDrawer";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, patchJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
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
export function DetectorPluginsPanel() {
  const { t, language } = useI18n();
  const queryClient = useQueryClient();
  const plugins = useJSON(apiPaths.detectorPlugins, []);
  const [busyID, setBusyID] = useState("");
  const [detailID, setDetailID] = useState("");
  const [query, setQuery] = useState("");
  // conflict holds the server's 409 payload: the mapping count that must be
  // acknowledged before a hot-lane plugin stops masking new values.
  const [conflict, setConflict] = useState(null);

  const invalidateCatalog = () => {
    queryClient.invalidateQueries({ queryKey: ["GET", apiPaths.detectorPlugins] });
  };

  const enable = useWriteMutation({
    mutationFn: (item) => patchJSON(apiPaths.detectorPlugin(item.id), { enabled: true }),
    success: "plugins.enabledToast",
    invalidate: [["GET", apiPaths.detectorPlugins]],
  });

  // The settings write replaces the plugin's opaque settings object, so the
  // caller merges over the current value (detectorPIISettings) to keep the
  // keys it does not own, such as disabled_rules.
  const saveSettings = useWriteMutation({
    mutationFn: ({ item, settings }) => patchJSON(apiPaths.detectorPlugin(item.id), { settings }),
    success: "plugins.detectionPiiSaved",
    invalidate: [["GET", apiPaths.detectorPlugins]],
  });

  const requestDisable = async (item, force) => {
    setBusyID(item.id);
    try {
      await patchJSON(apiPaths.detectorPlugin(item.id), { enabled: false, force: Boolean(force) });
      toast.success(t("plugins.disabledToast"));
      setConflict(null);
      invalidateCatalog();
    } catch (err) {
      if (err?.status === 409 && err?.payload?.error === "plugin_in_use") {
        setConflict({
          plugin: item,
          mappingCount: err.payload.mapping_count || 0,
        });
      } else {
        toast.error(err?.message || t("common.actionFailed"));
      }
    } finally {
      setBusyID("");
    }
  };

  const onToggle = (item, next) => {
    if (next) {
      enable.mutate(item);
      return;
    }
    // The server owns the check; the non-force attempt returns the mapping
    // count when the vault still holds stand-ins, and succeeds directly
    // otherwise.
    requestDisable(item, false);
  };

  const items = plugins.data?.items || [];
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
      </div>
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
              <Switch
                checked={Boolean(item.enabled)}
                disabled={busyID === item.id || enable.isPending}
                onChange={(next) => onToggle(item, next)}
                label={t("plugins.enableSwitch")}
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

      {conflict ? (
        <Dialog open onOpenChange={(next) => (next ? undefined : setConflict(null))}>
          <DialogContent>
            <DialogHeader>
              <div>
                <p className="eyebrow">{t("plugins.disableCheck")}</p>
                <DialogTitle>{t("plugins.disableConfirmTitle", { name: localizedName(conflict.plugin, language) })}</DialogTitle>
              </div>
            </DialogHeader>
            <p className="trace-subline">{t("plugins.detectionDisableWarning")}</p>
            <p className="trace-subline">{t("plugins.detectionMappingCount", { count: conflict.mappingCount })}</p>
            <DialogFooter>
              <Button variant="ghost" type="button" onClick={() => setConflict(null)}>{t("common.cancel")}</Button>
              <Button
                variant="primary"
                type="button"
                disabled={busyID === conflict.plugin.id}
                onClick={() => requestDisable(conflict.plugin, true)}
              >
                {t("plugins.disableAnyway")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </>
  );
}
