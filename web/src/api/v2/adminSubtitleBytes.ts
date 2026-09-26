import {
  captureProfileRequestContext,
  fetchWithSession,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";

// This raw attachment boundary owns status/body handling; JSON v2 decoding does not apply.
export async function downloadAdminSubtitle(
  subtitle: AdminStoredSubtitle,
  scope: string,
): Promise<void> {
  const context = captureProfileRequestContext();
  const requireAuthority = () => {
    if (
      !context ||
      !isCapturedProfileAuthorityActive(context) ||
      adminSubtitleListScope() !== scope
    )
      throw new StaleApiRequestContextError();
  };
  requireAuthority();
  if (!/^[1-9]\d*$/.test(subtitle.id)) throw new Error("Invalid subtitle ID.");
  const { res } = await fetchWithSession(
    `/api/v2/admin/subtitles/${subtitle.id}/download`,
    {
      method: "GET",
      headers: { Accept: "application/x-subrip, text/vtt, text/x-ssa, application/octet-stream" },
    },
    context ?? undefined,
    false,
  );
  requireAuthority();
  if (res.status !== 200) throw new Error(`Subtitle download failed (${res.status}).`);
  const media = res.headers.get("Content-Type")?.split(";")[0]?.trim().toLowerCase();
  if (
    !media ||
    !["application/x-subrip", "text/vtt", "text/x-ssa", "application/octet-stream"].includes(media)
  )
    throw new Error("Unexpected subtitle download response.");
  const blob = await res.blob();
  requireAuthority();
  // A deterministic filename avoids interpreting an untrusted Content-Disposition header.
  const format = ["srt", "vtt", "ass", "ssa", "sub"].includes(subtitle.format)
    ? subtitle.format
    : "bin";
  const url = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `subtitle-${subtitle.id}.${format}`;
    anchor.click();
  } finally {
    URL.revokeObjectURL(url);
  }
}
