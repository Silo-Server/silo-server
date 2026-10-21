/**
 * Reads that hand back the validator a later guarded write must send as
 * If-Match. Components never read response headers; they keep the returned
 * `etag` beside the body it describes.
 */
import { v2, type V2OperationKey, type V2RequestArgs, type V2Result } from "./request";

/** The validator to send as If-Match. Throws when a guarded read had none to give. */
export function requiredETag(etag: string | undefined | null): string {
  if (!etag || etag === "*")
    throw new Error("Reload this collection before editing; its version is unavailable.");
  return etag;
}

/** One v2 request that returns its decoded body and its ETag, or throws when the ETag is missing. */
export async function withETag<K extends V2OperationKey>(
  key: K,
  ...args: V2RequestArgs<K>
): Promise<{ body: V2Result<K>; etag: string }> {
  const options = args[0];
  let etag: string | null = null;
  const body = await v2(
    key,
    ...([
      {
        ...options,
        onResponse: (response: Response) => {
          etag = response.headers.get("ETag");
          options?.onResponse?.(response);
        },
      },
    ] as V2RequestArgs<K>),
  );
  return { body, etag: requiredETag(etag) };
}
