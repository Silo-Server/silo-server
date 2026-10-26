import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";

// Browser navigation cannot send headers. An ordinary API request, which does
// carry the selected profile and its PIN proof, mints a short-lived link bound
// to that profile and file; the navigation URL carries only that link, never
// the account access token.
export async function launchDirectDownload(
  fileId: number,
  isCurrent: () => boolean,
): Promise<void> {
  if (!Number.isSafeInteger(fileId) || fileId <= 0) throw new Error("Invalid file ID.");
  const profileContext = captureProfileRequestContext();
  const requireCurrent = () => {
    if (!profileContext || !isCurrent() || !isCapturedProfileAuthorityActive(profileContext))
      throw new StaleApiRequestContextError();
  };
  requireCurrent();
  let url: string;
  let res: Response;
  try {
    const link = await v2("POST /api/v2/direct-download/links", {
      body: { file_id: String(fileId) },
      profileContext: profileContext!,
    });
    requireCurrent();
    url = link.url;
    // One probe only: no refresh replay, link replacement or proxy URL invention.
    res = await fetch(url, { method: "HEAD", cache: "no-store" });
  } catch (error) {
    // A rejected mint or probe must not report into a replacement authority either.
    requireCurrent();
    throw error;
  }
  requireCurrent();
  if (res.status !== 200) throw new Error(`Download unavailable (${res.status}).`);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "";
  anchor.referrerPolicy = "no-referrer";
  // HEAD is only an observation. The browser GET independently reauthorizes;
  // launch is not proof of successful transfer or durable local storage.
  anchor.click();
}
