import { captureProfileRequestContext, type ProfileRequestContextSnapshot } from "@/api/client";
import { v2 } from "@/api/v2/request";

/** One open reader's configuration validator and captured profile authority. */
export type EbookReaderConfigSession = {
  profileContext: ProfileRequestContextSnapshot | null;
  etag: string;
  pending: Promise<void>;
};

export function createEbookReaderConfigSession(): EbookReaderConfigSession {
  return { profileContext: captureProfileRequestContext(), etag: "", pending: Promise.resolve() };
}

export async function fetchEbookReaderConfig(
  contentID: string,
  session: EbookReaderConfigSession,
): Promise<Record<string, unknown>> {
  if (!session.profileContext) throw new Error("Reader configuration requires a profile.");
  let etag = "";
  const loading = v2("GET /api/v2/ebooks/{content_id}/reader-config", {
    path: { content_id: contentID },
    profileContext: session.profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? "";
    },
  }).then((result) => {
    if (!etag) throw new Error("The reader configuration has no validator.");
    session.etag = etag;
    return result.config;
  });
  session.pending = loading.then(() => undefined);
  // The caller owns load errors; a later queued write still sees the failure.
  void session.pending.catch(() => undefined);
  return loading;
}

export function saveEbookReaderConfig(
  contentID: string,
  config: Record<string, unknown>,
  session: EbookReaderConfigSession,
  keepalive = false,
): Promise<Record<string, unknown>> {
  const body = JSON.parse(JSON.stringify({ config })) as { config: Record<string, unknown> };
  const saving = session.pending.then(async () => {
    if (!session.profileContext || !session.etag)
      throw new Error("Reader configuration has not loaded.");
    let etag = "";
    const result = await v2("PUT /api/v2/ebooks/{content_id}/reader-config", {
      path: { content_id: contentID },
      body,
      headers: { "If-Match": session.etag },
      profileContext: session.profileContext,
      keepalive,
      retryAuthentication: !keepalive,
      onResponse: (response) => {
        etag = response.headers.get("ETag") ?? "";
      },
    });
    if (!etag) throw new Error("The reader configuration has no validator.");
    session.etag = etag;
    return result.config;
  });
  // A conflict or uncertain response fences later queued edits until reload.
  // Never obtain a newer validator merely to overwrite another editor.
  session.pending = saving.then(() => undefined);
  void session.pending.catch(() => undefined);
  return saving;
}

export function saveEbookReaderConfigKeepalive(
  contentID: string,
  config: Record<string, unknown>,
  session: EbookReaderConfigSession,
): void {
  void saveEbookReaderConfig(contentID, config, session, true).catch(() => undefined);
}
