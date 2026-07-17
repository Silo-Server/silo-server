import type { AdminSession } from "@/api/types";
import { isJellyfinSession } from "@/pages/adminActivityPresentation";

/**
 * Purple "JF" pill marking a session served through the Jellyfin compatibility
 * surface (server-identified via is_jellyfin_client). Renders nothing for
 * native sessions, so callers can drop it into any badge row unconditionally.
 */
export function JellyfinSessionPill({ session }: { session: AdminSession }) {
  if (!isJellyfinSession(session)) {
    return null;
  }
  return (
    <span
      className="inline-flex flex-shrink-0 rounded border border-[#AA5CC3]/30 bg-[#AA5CC3]/15 px-1.5 py-0.5 text-[9px] font-semibold text-[#AA5CC3]"
      title="Jellyfin client"
    >
      JF
    </span>
  );
}
