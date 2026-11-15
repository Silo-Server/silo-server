import type { PluginInstallation } from "@/api/types";
import { t } from "@/i18n";

/**
 * Status dot for an installation. A resident plugin (one the server keeps
 * running) reports its supervisor state. A plugin started on first use reads
 * Active unless its last launch failed.
 */
export function pluginStatusIndicator(installation: PluginInstallation): {
  dotClass: string;
  label: string;
  title?: string;
} {
  if (!installation.enabled) {
    return { dotClass: "bg-muted-foreground", label: t("web.admin.plugins.statusIndicator.off") };
  }
  const runtime = installation.runtime;
  if (!runtime.resident) {
    if (runtime.state === "failed") {
      return {
        dotClass: "bg-destructive",
        label: t("web.admin.plugins.statusIndicator.failed"),
        title: runtime.last_error,
      };
    }
    return { dotClass: "bg-success", label: t("web.admin.plugins.statusIndicator.active") };
  }
  switch (runtime.state) {
    case "running":
      return { dotClass: "bg-success", label: t("web.admin.plugins.statusIndicator.running") };
    case "starting":
      return { dotClass: "bg-warning", label: t("web.admin.plugins.statusIndicator.starting") };
    case "backoff":
      return {
        dotClass: "bg-warning",
        label: t("web.admin.plugins.statusIndicator.restarting", {
          restart_count: runtime.restart_count,
        }),
        title: runtime.last_error,
      };
    case "failed":
      return {
        dotClass: "bg-destructive",
        label: t("web.admin.plugins.statusIndicator.failed"),
        title: runtime.last_error,
      };
    default:
      return {
        dotClass: "bg-muted-foreground",
        label: t("web.admin.plugins.statusIndicator.stopped"),
        title: runtime.last_error,
      };
  }
}
