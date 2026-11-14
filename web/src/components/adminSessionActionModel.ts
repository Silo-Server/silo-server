import type { AdminSession } from "@/api/types";
import { t } from "@/i18n";

export type AdminSessionActionName = "pause" | "resume";

export function isSessionPaused(session: AdminSession): boolean {
  return Boolean(session.is_paused);
}

export function getPrimaryPlaybackAction(session: AdminSession): {
  action: AdminSessionActionName;
  label: string;
} {
  if (isSessionPaused(session)) {
    return { action: "resume", label: t("web.admin.sessions.playback.action.resume") };
  }
  return { action: "pause", label: t("web.admin.sessions.playback.action.pause") };
}
