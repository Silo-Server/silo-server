import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { adminKeys, themeKeys } from "../keys";

export type BrandingAssetKind =
  | "wordmark"
  | "mark"
  | "wordmark_light"
  | "mark_light"
  | "favicon"
  | "login_bg";

/** A branding asset write, captured with the authority it was requested under. */
type BrandingAssetIntent = {
  kind: BrandingAssetKind;
  file?: File;
  authority: ProfileRequestContextSnapshot | null;
};
function brandingIntent(kind: BrandingAssetKind, file?: File): BrandingAssetIntent {
  return { kind, file, authority: captureProfileRequestContext() };
}
function authorityActive(intent: BrandingAssetIntent): boolean {
  return intent.authority !== null && isCapturedProfileAuthorityActive(intent.authority);
}

/** Invalidates the public branding read and the admin settings map. */
function invalidateBranding(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: themeKeys.branding() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
  ]);
}

/**
 * Uploads a branding image (multipart) for the given asset kind. Both writes are
 * non_retryable: the server assigns the slot unconditionally, so a delayed replay
 * would overwrite a newer choice. The intent captures kind, file and authority at
 * click time, sends once, and refuses to run after the authority changed.
 */
export function useUploadBrandingAsset() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: BrandingAssetIntent) => {
      if (!intent.file || !authorityActive(intent)) throw new StaleApiRequestContextError();
      return v2("POST /api/v2/admin/branding/assets/{kind}", {
        path: { kind: intent.kind },
        form: { file: intent.file },
        profileContext: intent.authority ?? undefined,
        retryAuthentication: false,
      });
    },
    onSuccess: (_result, intent) => {
      if (authorityActive(intent)) void invalidateBranding(queryClient);
    },
    onError: (err, intent) => {
      if (authorityActive(intent))
        toast.error(err instanceof Error ? err.message : "Failed to upload image");
    },
  });
  return {
    ...mutation,
    mutate: ({ kind, file }: { kind: BrandingAssetKind; file: File }) =>
      mutation.mutate(brandingIntent(kind, file)),
  };
}

/** Removes the custom branding asset of the given kind. */
export function useDeleteBrandingAsset() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: BrandingAssetIntent) => {
      if (!authorityActive(intent)) throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/branding/assets/{kind}", {
        path: { kind: intent.kind },
        profileContext: intent.authority ?? undefined,
        retryAuthentication: false,
      });
    },
    onSuccess: (_result, intent) => {
      if (authorityActive(intent)) void invalidateBranding(queryClient);
    },
    onError: (err, intent) => {
      if (authorityActive(intent))
        toast.error(err instanceof Error ? err.message : "Failed to remove image");
    },
  });
  return {
    ...mutation,
    mutate: ({ kind }: { kind: BrandingAssetKind }) => mutation.mutate(brandingIntent(kind)),
  };
}
