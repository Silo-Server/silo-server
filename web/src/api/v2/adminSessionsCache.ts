import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope } from "./adminAuthority";

export function adminSessionsKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.sessions(), adminAuthorityScope(context)] as const;
}
