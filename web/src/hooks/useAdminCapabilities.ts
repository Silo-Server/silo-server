import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { AdminCapabilities } from "@/api/types";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { adminKeys } from "@/hooks/queries/keys";

const ADMIN_CAPABILITIES_STALE_TIME = 30_000;

export function useAdminCapabilities() {
  const user = useOptionalAuth()?.user;
  return useQuery({
    queryKey: adminKeys.capabilities(),
    queryFn: () => api<AdminCapabilities>("/auth/admin-capabilities"),
    enabled: Boolean(user),
    staleTime: ADMIN_CAPABILITIES_STALE_TIME,
  });
}

export function useAdminAccess() {
  const actingAdmin = useIsActingAdmin();
  const capabilities = useAdminCapabilities();
  const actionSet = useMemo(
    () => new Set(capabilities.data?.actions ?? []),
    [capabilities.data?.actions],
  );

  return {
    actingAdmin,
    actions: capabilities.data?.actions ?? [],
    actionSet,
    isLoading: !actingAdmin && capabilities.isLoading,
    canAccessAdmin: actingAdmin || actionSet.size > 0,
    can: (action: string | readonly string[]) => {
      if (actingAdmin) return true;
      const actions = Array.isArray(action) ? action : [action];
      return actions.some((value) => actionSet.has(value));
    },
  };
}
