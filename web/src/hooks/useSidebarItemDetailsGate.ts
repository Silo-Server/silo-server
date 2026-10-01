import { useCallback, useState } from "react";

interface PreparedItemNavigation {
  originLocationKey: string;
  itemRouteLocation: string;
  hadCachedDetails: boolean;
}

interface ItemDetailsGateState {
  locationKey: string;
  pathname: string;
  gatesItemDetails: boolean;
  pendingLocationKey: string | null;
  enteredItemFromHome: boolean;
  animateHomeItemEntry: boolean;
  itemChainStartedFromHome: boolean;
  returnedHomeFromItem: boolean;
  preparedItemNavigation: PreparedItemNavigation | null;
}

interface ItemDetailsGateOptions {
  itemRouteLocation: string;
  itemDetailsAvailableOnEntry: boolean;
}

/**
 * Keeps item details lightweight while a desktop route entry collapses the
 * sidebar. The gate follows committed route transitions instead of individual
 * click handlers, so POP and programmatic navigation receive the same
 * treatment and abandoned navigation cannot leave a stale global lock.
 */
export function useSidebarItemDetailsGate(
  locationKey: string,
  pathname: string,
  gatesItemDetails: boolean,
  options: ItemDetailsGateOptions = {
    itemRouteLocation: pathname,
    itemDetailsAvailableOnEntry: false,
  },
) {
  const [state, setState] = useState<ItemDetailsGateState>({
    locationKey,
    pathname,
    gatesItemDetails,
    pendingLocationKey: null,
    enteredItemFromHome: false,
    animateHomeItemEntry: false,
    itemChainStartedFromHome: false,
    returnedHomeFromItem: false,
    preparedItemNavigation: null,
  });

  let currentState = state;
  if (
    state.locationKey !== locationKey ||
    state.pathname !== pathname ||
    state.gatesItemDetails !== gatesItemDetails
  ) {
    const enteringItem = gatesItemDetails && !state.gatesItemDetails;
    const returnedHomeFromItem =
      !gatesItemDetails &&
      state.gatesItemDetails &&
      pathname === "/" &&
      state.itemChainStartedFromHome;
    const enteredItemFromHome = enteringItem && state.pathname === "/";
    const preparedItemNavigation = state.preparedItemNavigation;
    const hasPreparedEntry =
      enteringItem &&
      preparedItemNavigation?.originLocationKey === state.locationKey &&
      preparedItemNavigation.itemRouteLocation === options.itemRouteLocation;
    const itemDetailsAvailableOnEntry = hasPreparedEntry
      ? preparedItemNavigation.hadCachedDetails
      : options.itemDetailsAvailableOnEntry;
    currentState = {
      locationKey,
      pathname,
      gatesItemDetails,
      pendingLocationKey: enteringItem && !itemDetailsAvailableOnEntry ? locationKey : null,
      enteredItemFromHome,
      animateHomeItemEntry: enteredItemFromHome && !itemDetailsAvailableOnEntry,
      itemChainStartedFromHome: enteringItem
        ? state.pathname === "/"
        : gatesItemDetails && state.itemChainStartedFromHome,
      returnedHomeFromItem,
      preparedItemNavigation: null,
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

  const prepareItemNavigation = useCallback(
    (itemRouteLocation: string, hadCachedDetails: boolean) => {
      setState((current) => ({
        ...current,
        preparedItemNavigation: {
          originLocationKey: current.locationKey,
          itemRouteLocation,
          hadCachedDetails,
        },
      }));
    },
    [],
  );

  return {
    itemDetailsReady: !gatesItemDetails || currentState.pendingLocationKey === null,
    pendingLocationKey: currentState.pendingLocationKey,
    enteredItemFromHome: currentState.enteredItemFromHome,
    animateHomeItemEntry: currentState.animateHomeItemEntry,
    returnedHomeFromItem: currentState.returnedHomeFromItem,
    prepareItemNavigation,
    reveal,
  };
}
