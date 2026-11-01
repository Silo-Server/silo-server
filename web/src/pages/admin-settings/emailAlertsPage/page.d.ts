export type EmailAlertsPageAPI = (
  path: string,
  opts?: { method?: string; body?: unknown },
) => Promise<unknown>;

/** Mounts the email alerts page into root; returns the unmount cleanup. */
export function mountEmailAlertsPage(root: ShadowRoot, api: EmailAlertsPageAPI): () => void;
