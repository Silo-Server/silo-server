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

/**
 * Whether the viewer can request the seasons a series in the library is
 * missing. The server allows it only while no download server takes series,
 * since router plugins cannot receive seasons yet.
 */
export function useMissingSeasonsRequestable(enabled: boolean): boolean {
  // The shell's sidebar keeps the status fresh; opening a series page reads
  // what it has rather than fetching again.
  const status = useRequestFeatureStatus({ enabled, refetchOnMount: false });
  const data = status.data;
  return (
    enabled && Boolean(data?.requests_enabled && data.allowed && data.missing_seasons_requestable)
  );
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
