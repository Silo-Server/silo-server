import { useAdminServerSettings } from "@/hooks/queries/admin/settings";

/**
 * The two server-wide download switches, read from the settings the Downloads
 * settings page edits so a notice never disagrees with its toggles. Both stay
 * false until the settings have loaded: a notice shows only for a switch known
 * to be off.
 */
export function useServerDownloadSwitches() {
  const { data } = useAdminServerSettings();
  return {
    downloadsOff: data !== undefined && data["download.enabled"] !== "true",
    conversionOff: data !== undefined && data["download.transcode_enabled"] !== "true",
  };
}
