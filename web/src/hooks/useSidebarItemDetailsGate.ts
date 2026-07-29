import { useCallback, useState } from "react";

interface ItemDetailsGateState {
  locationKey: string;
  gatesItemDetails: boolean;
  pendingLocationKey: string | null;
}

/**
 * Keeps item details lightweight while a desktop route entry collapses the
 * sidebar. The gate follows committed route transitions instead of individual
 * click handlers, so POP and programmatic navigation receive the same
 * treatment and abandoned navigation cannot leave a stale global lock.
 */
export function useSidebarItemDetailsGate(locationKey: string, gatesItemDetails: boolean) {
  const [state, setState] = useState<ItemDetailsGateState>({
    locationKey,
    gatesItemDetails,
    pendingLocationKey: null,
  });

  let currentState = state;
  if (state.locationKey !== locationKey || state.gatesItemDetails !== gatesItemDetails) {
    currentState = {
      locationKey,
      gatesItemDetails,
      pendingLocationKey: gatesItemDetails && !state.gatesItemDetails ? locationKey : null,
    };
    // React discards this render and retries before rendering children, so the
    // item route never briefly receives `itemDetailsReady=true` on entry.
    setState(currentState);
  }

  const reveal = useCallback((expectedLocationKey: string) => {
    setState((current) =>
      current.pendingLocationKey === expectedLocationKey
        ? { ...current, pendingLocationKey: null }
        : current,
    );
  }, []);

  return {
    itemDetailsReady: !gatesItemDetails || currentState.pendingLocationKey === null,
    pendingLocationKey: currentState.pendingLocationKey,
    reveal,
  };
}
