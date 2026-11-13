import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { apiPaths, patchJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";

/**
 * The enable/disable interaction shared by the detection-plugin page and the
 * XIR structure map's plugin rail.
 *
 * Both screens write the same PATCH and both must honour the same two answers:
 * a 409 `plugin_in_use` carries the stand-in mapping count and needs the force
 * confirmation, and a 400 `plugin_builtin` means the capability cannot be
 * switched off at all. Keeping one hook for the flow is what keeps an
 * always-on plugin from rendering a switch in one screen and a badge in the
 * other.
 */
export function useDetectorPluginToggle() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [busyID, setBusyID] = useState("");
  const [conflict, setConflict] = useState(null);

  const invalidateCatalog = () => {
    queryClient.invalidateQueries({ queryKey: ["GET", apiPaths.detectorPlugins] });
  };

  const enable = useWriteMutation({
    mutationFn: (item) => patchJSON(apiPaths.detectorPlugin(item.id), { enabled: true }),
    success: "plugins.enabledToast",
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
        setConflict({ plugin: item, mappingCount: err.payload.mapping_count || 0 });
      } else {
        // The server's message explains why (e.g. plugin_builtin); the error
        // code alone would leave the reader without a reason.
        toast.error(err?.payload?.message || err?.message || t("common.actionFailed"));
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
    requestDisable(item, false);
  };

  return { busyID, conflict, setConflict, enable, requestDisable, onToggle };
}
