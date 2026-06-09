import type { JellyfinCompatStatus } from "@/api/types";

type JellyfinWebInstallStatus = Pick<
  JellyfinCompatStatus,
  "installed_version" | "pinned_version" | "web_state"
>;

export function normalizeJellyfinCompatVersion(version?: string | null): string {
  return version?.trim().replace(/^[vV]/, "") ?? "";
}

export function hasPinnedJellyfinWebInstalled(status?: JellyfinWebInstallStatus | null): boolean {
  if (status?.web_state !== "installed") return false;

  const installedVersion = normalizeJellyfinCompatVersion(status.installed_version);
  const pinnedVersion = normalizeJellyfinCompatVersion(status.pinned_version);

  return installedVersion !== "" && pinnedVersion !== "" && installedVersion === pinnedVersion;
}
