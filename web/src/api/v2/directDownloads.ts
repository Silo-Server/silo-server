import {
  captureProfileRequestContext,
  captureSessionIdentity,
  getAccessToken,
  isCapturedProfileAuthorityActive,
  isSessionIdentityCurrent,
  StaleApiRequestContextError,
} from "@/api/client";

// Browser navigation cannot set headers. Retain the existing account-token
// authority; do not imply that the selected profile/PIN travels in this URL.
export async function launchDirectDownload(
  fileId: number,
  isCurrent: () => boolean,
): Promise<void> {
  if (!Number.isSafeInteger(fileId) || fileId <= 0) throw new Error("Invalid file ID.");
  const token = getAccessToken();
  const identity = captureSessionIdentity();
  const profile = captureProfileRequestContext();
  const requireCurrent = () => {
    if (
      !token ||
      !isCurrent() ||
      !isSessionIdentityCurrent(identity) ||
      (profile && !isCapturedProfileAuthorityActive(profile))
    )
      throw new StaleApiRequestContextError();
  };
  requireCurrent();
  const url = `/api/v2/direct-download?${new URLSearchParams({ file_id: String(fileId), token: token! })}`;
  // One probe only: no refresh replay, token replacement or proxy URL invention.
  let res: Response;
  try {
    res = await fetch(url, { method: "HEAD", cache: "no-store" });
  } catch (error) {
    // A rejected probe must not report into a replacement authority either.
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
