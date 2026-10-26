import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope } from "./adminAuthority";

// Kept apart from adminDownloadStorage so the realtime provider, which loads
// with the app, can refresh these queries without pulling the storage API into
// the launch bundle.
export function adminDownloadStorageRootKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.downloadStorage(), adminAuthorityScope(context)] as const;
}
