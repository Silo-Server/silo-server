import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

export interface CanRequestState {
  discoveryEnabled: boolean;
  submitDisabledReason: string | null;
}

export function useCanRequest(): CanRequestState {
  const status = useRequestFeatureStatus();
  const { profile } = useCurrentProfile();
  const discoveryEnabled = Boolean(status.data?.requests_enabled) && Boolean(profile?.id);

  return {
    discoveryEnabled,
    submitDisabledReason: null,
  };
}
