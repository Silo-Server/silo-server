import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import { adminKeys, themeKeys } from "../keys";

export type BrandingAssetKind = "wordmark" | "mark" | "favicon" | "login_bg";

interface BrandingAssetUploadResponse {
  kind: string;
  ref: string;
  url: string;
}

/** Invalidates the public branding read and the admin settings map. */
function invalidateBranding(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: themeKeys.branding() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
  ]);
}

/** Uploads a branding image (multipart) for the given asset kind. */
export function useUploadBrandingAsset() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ kind, file }: { kind: BrandingAssetKind; file: File }) => {
      const form = new FormData();
      form.append("file", file);
      return api<BrandingAssetUploadResponse>(`/admin/branding/assets/${kind}`, {
        method: "POST",
        body: form,
      });
    },
    onSuccess: () => invalidateBranding(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to upload image");
    },
  });
}

/** Removes the custom branding asset of the given kind. */
export function useDeleteBrandingAsset() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ kind }: { kind: BrandingAssetKind }) =>
      api<void>(`/admin/branding/assets/${kind}`, { method: "DELETE" }),
    onSuccess: () => invalidateBranding(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove image");
    },
  });
}
