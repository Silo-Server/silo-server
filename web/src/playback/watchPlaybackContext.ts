import { createContext, useContext } from "react";
import type { PlaybackStartTrigger, PlayerPictureInPictureChange } from "@/player";
import type { WatchPlaybackStartInput, WatchRouteRequest } from "@/pages/watchRouteHelpers";
import type {
  WatchPlaybackHostState,
  WatchPlaybackTransportControls,
} from "./watchPlaybackReducer";
import type { WatchPlaybackSnapshot } from "./watchPlaybackSnapshotStore";

export interface WatchPlaybackControllerValue {
  state: WatchPlaybackHostState;
  hasDetachedPlayback: boolean;
  isBackgroundBarVisible: boolean;
  /**
   * Starts a request. `trigger` says whether the viewer asked for it here
   * (`viewer`), which times its first frame, or the app started it on its own
   * (`automatic`), which does not.
   */
  startPlayback: (
    input: WatchPlaybackStartInput | WatchRouteRequest,
    trigger: PlaybackStartTrigger,
  ) => void;
  minimizePlayback: () => void;
  exitPlayback: (options?: { destinationHref?: string }) => void;
  stopPlayback: () => void;
  enterPostRoll: (requestKey: string) => void;
  returnToWatch: () => void;
  syncRouteRequest: (request: WatchRouteRequest) => void;
  handleRouteExit: (requestKey: string) => void;
  setPictureInPictureActive: (requestKey: string, change: PlayerPictureInPictureChange) => void;
  clearPendingReturnNavigation: (requestKey: string) => void;
  updatePlaybackSnapshot: (requestKey: string, snapshot: WatchPlaybackSnapshot) => void;
  setTransportControls: (
    requestKey: string,
    controls: WatchPlaybackTransportControls | null,
  ) => void;
}

export const WatchPlaybackControllerContext = createContext<WatchPlaybackControllerValue | null>(
  null,
);

export function useWatchPlaybackController() {
  const context = useContext(WatchPlaybackControllerContext);
  if (!context) {
    throw new Error("Watch playback controller is unavailable outside WatchPlaybackProvider");
  }
  return context;
}
