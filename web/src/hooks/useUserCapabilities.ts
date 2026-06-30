import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { UserCapabilities } from "@/api/types";
import { authKeys } from "@/hooks/queries/keys";
import { useOptionalAuth } from "@/hooks/useAuth";

const USER_CAPABILITIES_STALE_TIME = 30_000;

export const USER_CAPABILITY_ACTIONS = {
  playbackPlay: "playback.play",
  playbackTranscode: "playback.transcode",
  downloadsDirect: "downloads.direct",
  downloadsTranscode: "downloads.transcode",
  profilesManage: "profiles.manage",
  personalListsManage: "personal_lists.manage",
  requestsCreate: "requests.create",
} as const;

export function useUserCapabilities() {
  const user = useOptionalAuth()?.user;
  return useQuery({
    queryKey: authKeys.capabilities(),
    queryFn: () => api<UserCapabilities>("/auth/capabilities"),
    enabled: Boolean(user),
    staleTime: USER_CAPABILITIES_STALE_TIME,
  });
}

export function useUserCapabilityAccess() {
  const user = useOptionalAuth()?.user;
  const capabilities = useUserCapabilities();
  const actions = capabilities.data?.actions ?? [];
  const actionSet = useMemo(() => new Set(actions), [actions]);

  return {
    actions: user ? actions : Object.values(USER_CAPABILITY_ACTIONS),
    actionSet,
    isLoading: Boolean(user) && capabilities.isLoading,
    can: (action: string | readonly string[]) => {
      if (!user) return true;
      if (!capabilities.data && capabilities.isLoading) return false;
      const requestedActions = Array.isArray(action) ? action : [action];
      return requestedActions.some((value) => actionSet.has(value));
    },
  };
}
