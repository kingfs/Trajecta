import React from "react";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { useI18n } from "../../lib/i18n";
import { localizedName } from "../../lib/plugins";

/**
 * The 409 `plugin_in_use` confirmation shared by the plugin page and the XIR
 * structure map's rail. The server owns the check; this dialog only carries the
 * mapping count and the force decision.
 */
export function DetectorPluginConflictDialog({ conflict, busy = false, onCancel, onConfirm }) {
  const { t, language } = useI18n();
  if (!conflict) return null;
  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onCancel?.())}>
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
          <Button variant="ghost" type="button" onClick={onCancel}>{t("common.cancel")}</Button>
          <Button variant="primary" type="button" disabled={busy} onClick={onConfirm}>
            {t("plugins.disableAnyway")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
