import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { adminUserScope } from "./adminUsers";

export function adminSessionsKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.sessions(), adminUserScope(context)] as const;
}
