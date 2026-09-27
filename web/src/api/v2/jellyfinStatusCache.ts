import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";

export function jellyfinCompatStatusKey(context: ProfileRequestContextSnapshot) {
  return [
    ...adminKeys.jellyfinCompatStatus(),
    context.serverOrigin,
    context.authContextVersion,
    context.profileId,
  ] as const;
}
