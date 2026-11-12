import React, { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, KeyRound } from "lucide-react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { Switch } from "../../components/common/Controls";
import { PluginDetailDrawer } from "../../components/monitor/PluginDetailDrawer";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, patchJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { localizedName, localizedDescription, pluginDrawerFacts, filterPlugins } from "../../lib/plugins";

// ChannelPluginsPanel presents what each vendor plugin offers: name,
// description, docs/key links and the enable switch. Clicking a card opens the
// detail drawer (interfaces, API bases, plans). Channel-association facts are
// deliberately absent here; only the disable check queries them, and the
// server's 409 payload drives that confirmation.
export function ChannelPluginsPanel() {
  const { t, language } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const plugins = useJSON(apiPaths.plugins, []);
  const [busyID, setBusyID] = useState("");
  const [detailID, setDetailID] = useState("");
  const [query, setQuery] = useState("");
  // conflict holds the server's 409 payload: the enabled channels that must be
  // acknowledged before the plugin's catalog cards are hidden.
  const [conflict, setConflict] = useState(null);

  const invalidateCatalog = () => {
    queryClient.invalidateQueries({ queryKey: ["GET", apiPaths.plugins] });
    queryClient.invalidateQueries({ queryKey: ["GET", apiPaths.providerCatalog] });
  };

  const enable = useWriteMutation({
    mutationFn: (item) => patchJSON(apiPaths.plugin(item.id), { enabled: true }),
    success: "plugins.enabledToast",
    invalidate: [["GET", apiPaths.plugins], ["GET", apiPaths.providerCatalog]],
  });

  const requestDisable = async (item, force) => {
    setBusyID(item.id);
    try {
      await patchJSON(apiPaths.plugin(item.id), { enabled: false, force: Boolean(force) });
      toast.success(t("plugins.disabledToast"));
      setConflict(null);
      invalidateCatalog();
    } catch (err) {
      if (err?.status === 409 && err?.payload?.error === "plugin_in_use") {
        setConflict({
          plugin: item,
          channels: Array.isArray(err.payload.channels) ? err.payload.channels : [],
          enabledChannelCount: err.payload.enabled_channel_count || 0,
          enabledModelCount: err.payload.enabled_model_count || 0,
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
    // The server owns the check; the non-force attempt returns the affected
    // channels when any are enabled, and succeeds directly otherwise.
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
    return <EmptyState title={t("plugins.loading")} detail={t("plugins.loadingDetail")} />;
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
                {item.region ? <InlineTag tone="default">{t(`plugins.region.${item.region}`)}</InlineTag> : null}
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
              <div className="plugin-links" onClick={(event) => event.stopPropagation()}>
                {item.docs_url ? (
                  <a href={item.docs_url} target="_blank" rel="noreferrer">
                    <ExternalLink size={12} aria-hidden="true" />
                    <span>{t("plugins.docs")}</span>
                  </a>
                ) : null}
                {item.keys_url ? (
                  <a href={item.keys_url} target="_blank" rel="noreferrer">
                    <KeyRound size={12} aria-hidden="true" />
                    <span>{t("plugins.keys")}</span>
                  </a>
                ) : null}
              </div>
              <span className="plugin-card-more" aria-hidden="true">
                {t("plugins.details")}
                <ChevronRight size={13} />
              </span>
            </div>
          </Card>
        ))}
        {!items.length ? <EmptyState title={t("plugins.none")} detail={t("plugins.noneDetail")} /> : null}
        {items.length && !visibleItems.length ? <EmptyState title={t("plugins.searchEmpty")} detail={t("plugins.searchEmptyDetail")} /> : null}
      </div>

      <PluginDetailDrawer
        facts={pluginDrawerFacts(detailItem)}
        open={Boolean(detailItem)}
        onClose={() => { if (!conflict) setDetailID(""); }}
        enabled={Boolean(detailItem?.enabled)}
        onToggle={detailItem ? (next) => onToggle(detailItem, next) : undefined}
        toggleDisabled={busyID === detailID || enable.isPending}
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
            <p className="trace-subline">{t("plugins.disableWarning")}</p>
            <p className="trace-subline">
              {t("plugins.disableStats", {
                channels: conflict.enabledChannelCount,
                models: conflict.enabledModelCount,
              })}
            </p>
            <ul className="plugin-conflict-list">
              {conflict.channels.map((channel) => (
                <li key={channel.id}>
                  <strong>{channel.name || channel.id}</strong>
                  <span className="trace-subline">{t("plugins.conflictModels", { models: channel.enabled_model_count || 0 })}</span>
                </li>
              ))}
            </ul>
            <DialogFooter>
              <Button variant="ghost" type="button" onClick={() => setConflict(null)}>{t("common.cancel")}</Button>
              <Button variant="ghost" type="button" onClick={() => navigate("/channels")}>{t("plugins.goChannels")}</Button>
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