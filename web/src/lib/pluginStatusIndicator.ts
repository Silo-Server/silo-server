import type { PluginInstallation } from "@/api/types";

/**
 * Status dot for an installation. A resident plugin (one the server keeps
 * running) reports its supervisor state; everything else reads enabled.
 */
export function pluginStatusIndicator(installation: PluginInstallation): {
  dotClass: string;
  label: string;
  title?: string;
} {
  if (!installation.enabled) {
    return { dotClass: "bg-muted-foreground", label: "Inactive" };
  }
  const runtime = installation.runtime;
  if (!runtime.resident) {
    return { dotClass: "bg-success", label: "Active" };
  }
  switch (runtime.state) {
    case "running":
      return { dotClass: "bg-success", label: "Running" };
    case "starting":
      return { dotClass: "bg-warning", label: "Starting" };
    case "backoff":
      return {
        dotClass: "bg-warning",
        label: `Restarting (${runtime.restart_count})`,
        title: runtime.last_error,
      };
    case "failed":
      return { dotClass: "bg-destructive", label: "Failed", title: runtime.last_error };
    default:
      return { dotClass: "bg-muted-foreground", label: "Stopped", title: runtime.last_error };
  }
}
