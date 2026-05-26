import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

export interface CanRequestState {
  discoveryEnabled: boolean;
  /**
   * True while we cannot yet decide if discovery is on — feature status
   * is still loading. Consumers should suppress empty-state UI in this
   * window to avoid a flash before the request section appears.
   */
  isResolving: boolean;
  submitDisabledReason: string | null;
}

export function useCanRequest(): CanRequestState {
  const status = useRequestFeatureStatus();
  const { profile } = useCurrentProfile();
  const discoveryEnabled = Boolean(status.data?.requests_enabled) && Boolean(profile?.id);
  const isResolving = status.isLoading;

  return {
    discoveryEnabled,
    isResolving,
    submitDisabledReason: null,
  };
}
