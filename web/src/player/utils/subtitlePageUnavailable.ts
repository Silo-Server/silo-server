/**
 * True when a subtitle fetch response is the server's final
 * `subtitle_page_unavailable`: the track's progressive artifact cannot reach
 * this page inside its recovery budget, so the same request can only pay the
 * same container prefix again. The caller ends the track's fetch loop and
 * reports the failure once; scheduling another retry is the loop the server
 * stopped answering hopefully.
 */
export async function isSubtitlePageUnavailable(resp: Response): Promise<boolean> {
  if (resp.status !== 503) return false;
  try {
    const body = (await resp.json()) as {
      error?: string;
      type?: string;
      status?: number;
    };
    if (body?.error === "subtitle_page_unavailable") return true;
    return body?.type?.endsWith("/problems/subtitle_page_unavailable") === true;
  } catch {
    return false;
  }
}
