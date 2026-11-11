import React, { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../../components/ui/dialog";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { Switch } from "../../components/common/Controls";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, patchJSON } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useWriteMutation } from "../../lib/mutations";
import { localizedName, localizedDescription, planDisplayName } from "../../lib/plugins";

const PROTOCOL_LABEL_KEYS = {
  chat_completions: "providers.interfaceChat",
  responses: "providers.interfaceResponses",
  messages: "providers.interfaceMessages",
  gemini: "providers.interfaceGemini",
};

// ChannelPluginsPanel lists every compiled channel plugin with its enable
// switch. Disabling never touches routing: the dialog says so explicitly and
// recommends disabling the affected channels first when any are still enabled.
export function ChannelPluginsPanel() {
  const { t, language } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const plugins = useJSON(apiPaths.plugins, []);
  const [busyID, setBusyID] = useState("");
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

  if (plugins.error) {
    return <EmptyState title={t("plugins.loadError")} detail={plugins.error} tone="danger" />;
  }
  if (plugins.loading && !plugins.data) {
    return <EmptyState title={t("plugins.loading")} detail={t("plugins.loadingDetail")} />;
  }

  return (
    <>
      <p className="trace-subline plugins-intro">{t("plugins.intro")}</p>
      <div className="provider-grid">
        {items.map((item) => {
          const preset = item.preset || {};
          const plans = preset.plans || [];
          const protocols = preset.protocols || [];
          return (
            <Card key={item.id} className="plugin-card">
              <div className="panel-head">
                <div>
                  <h2>
                    {localizedName(item, language)}
                    {item.region ? <InlineTag tone="default">{t(`plugins.region.${item.region}`)}</InlineTag> : null}
                  </h2>
                  <p className="trace-subline">{item.id}</p>
                </div>
                <Switch
                  checked={Boolean(item.enabled)}
                  disabled={busyID === item.id || enable.isPending}
                  onChange={(next) => onToggle(item, next)}
                  label={t("plugins.enableSwitch")}
                />
              </div>
              <p className="trace-subline plugin-description">{localizedDescription(item, language)}</p>
              <div className="plugin-tags">
                <InlineTag tone="default">{t("plugins.defaultPlan")}</InlineTag>
                {plans.map((plan) => (
                  <InlineTag key={plan.id} tone={plan.coding ? "accent" : "neutral"} title={plan.key_prefix ? `${plan.key_prefix}…` : ""}>
                    {planDisplayName(plan, language)}
                  </InlineTag>
                ))}
              </div>
              <div className="plugin-tags">
                {protocols.map((protocol) => (
                  <InlineTag key={protocol} tone="default">{t(PROTOCOL_LABEL_KEYS[protocol] || protocol)}</InlineTag>
                ))}
              </div>
              <p className="trace-subline plugin-stats">
                {t("plugins.channelStats", {
                  channels: item.channel_count || 0,
                  enabled: item.enabled_channel_count || 0,
                  models: item.enabled_model_count || 0,
                })}
              </p>
              <div className="plugin-links">
                {item.docs_url ? <a href={item.docs_url} target="_blank" rel="noreferrer">{t("plugins.docs")}</a> : null}
                {item.keys_url ? <a href={item.keys_url} target="_blank" rel="noreferrer">{t("plugins.keys")}</a> : null}
              </div>
            </Card>
          );
        })}
        {!items.length ? <EmptyState title={t("plugins.none")} detail={t("plugins.noneDetail")} /> : null}
      </div>

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