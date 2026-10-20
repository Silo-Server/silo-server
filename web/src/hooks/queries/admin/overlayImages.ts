import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { settingsKeys } from "../keys";

/** An overlay image write, captured with the authority it was requested under. */
type OverlayImageIntent<T> = T & { authority: ProfileRequestContextSnapshot | null };

function intentOf<T>(input: T): OverlayImageIntent<T> {
  return { ...input, authority: captureProfileRequestContext() };
}

function authorityActive(intent: { authority: ProfileRequestContextSnapshot | null }): boolean {
  return intent.authority !== null && isCapturedProfileAuthorityActive(intent.authority);
}

/**
 * Adds an image to the overlay image library. Never retried: an upload is not
 * conditional, so a delayed replay could bring back an image the admin has
 * since deleted. The overlay config lists the library, so it refetches.
 */
export function useUploadOverlayImage() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: OverlayImageIntent<{ file: File; name: string }>) => {
      if (!authorityActive(intent)) throw new StaleApiRequestContextError();
      return v2("POST /api/v2/admin/overlay-images", {
        form: { file: intent.file, name: intent.name },
        profileContext: intent.authority ?? undefined,
        retryAuthentication: false,
      });
    },
    onSuccess: (_result, intent) => {
      if (authorityActive(intent)) {
        void queryClient.invalidateQueries({ queryKey: settingsKeys.overlayConfig() });
      }
    },
    onError: (err, intent) => {
      if (authorityActive(intent))
        toast.error(err instanceof Error ? err.message : "Failed to upload image");
    },
  });
  return {
    ...mutation,
    mutate: (input: { file: File; name: string }, options?: { onSuccess?: () => void }) =>
      mutation.mutate(intentOf(input), { onSuccess: () => options?.onSuccess?.() }),
  };
}

/** Removes an image from the library. Badges that used it fall back to text. */
export function useDeleteOverlayImage() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: OverlayImageIntent<{ ref: string }>) => {
      if (!authorityActive(intent)) throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/overlay-images/{ref}", {
        path: { ref: intent.ref },
        profileContext: intent.authority ?? undefined,
        retryAuthentication: false,
      });
    },
    onSuccess: (_result, intent) => {
      if (authorityActive(intent)) {
        void queryClient.invalidateQueries({ queryKey: settingsKeys.overlayConfig() });
      }
    },
    onError: (err, intent) => {
      if (authorityActive(intent))
        toast.error(err instanceof Error ? err.message : "Failed to remove image");
    },
  });
  return {
    ...mutation,
    mutate: (input: { ref: string }) => mutation.mutate(intentOf(input)),
  };
}
