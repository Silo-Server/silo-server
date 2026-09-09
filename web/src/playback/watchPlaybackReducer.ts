import type { WatchRouteRequest } from "@/pages/watchRouteHelpers";

export type WatchPlaybackMode =
  | "foreground"
  | "background-bar"
  | "picture-in-picture"
  | "post-roll";

export interface WatchPlaybackSnapshot {
  currentTime: number;
  duration: number;
  playing: boolean;
}

export type WatchPlaybackTransportControls = import("@/player/types").PlayerPlaybackTransport;

export interface WatchPlaybackHostState {
  request: WatchRouteRequest | null;
  mode: WatchPlaybackMode;
  pictureInPictureActive: boolean;
  pendingReturnNavigation: string | null;
  shouldReturnToWatchPage: boolean;
  autoEnterPictureInPicture: boolean;
  snapshot: WatchPlaybackSnapshot | null;
  transport: WatchPlaybackTransportControls | null;
  routeExitBypassRequestKey: string | null;
}

export type WatchPlaybackAction =
  | {
      type: "START_PLAYBACK";
      request: WatchRouteRequest;
      mode?: Extract<WatchPlaybackMode, "foreground" | "background-bar">;
    }
  | { type: "SYNC_ROUTE_REQUEST"; request: WatchRouteRequest }
  | { type: "ROUTE_LEFT"; requestKey: string }
  | { type: "EXIT_PLAYBACK" }
  | { type: "MINIMIZE_PLAYBACK"; requestKey: string }
  | { type: "RESTORE_PLAYBACK"; requestKey: string }
  | { type: "ENTER_PIP"; requestKey: string }
  | {
      type: "LEAVE_PIP";
      requestKey: string;
      playbackContinues: boolean;
      suppressed?: boolean;
    }
  | { type: "REQUEST_RETURN_TO_WATCH"; requestKey: string }
  | { type: "ENTER_POSTROLL"; requestKey: string }
  | { type: "STOP_PLAYBACK" }
  | { type: "CLEAR_PENDING_RETURN_NAVIGATION"; requestKey: string }
  | {
      type: "UPDATE_SNAPSHOT";
      requestKey: string;
      snapshot: WatchPlaybackSnapshot;
    }
  | {
      type: "SET_TRANSPORT";
      requestKey: string;
      transport: WatchPlaybackTransportControls | null;
    };

export function createEmptyPlaybackState(): WatchPlaybackHostState {
  return {
    request: null,
    mode: "foreground",
    pictureInPictureActive: false,
    pendingReturnNavigation: null,
    shouldReturnToWatchPage: false,
    autoEnterPictureInPicture: false,
    snapshot: null,
    transport: null,
    routeExitBypassRequestKey: null,
  };
}

function createPlaybackState(
  request: WatchRouteRequest,
  mode: Extract<WatchPlaybackMode, "foreground" | "background-bar"> = "foreground",
): WatchPlaybackHostState {
  return {
    request,
    mode,
    pictureInPictureActive: false,
    pendingReturnNavigation: null,
    shouldReturnToWatchPage: false,
    autoEnterPictureInPicture: false,
    snapshot: null,
    transport: null,
    routeExitBypassRequestKey: null,
  };
}

export function watchPlaybackReducer(
  state: WatchPlaybackHostState,
  action: WatchPlaybackAction,
): WatchPlaybackHostState {
  switch (action.type) {
    case "START_PLAYBACK":
      return createPlaybackState(action.request, action.mode ?? "foreground");

    case "SYNC_ROUTE_REQUEST":
      if (state.request?.requestKey === action.request.requestKey) {
        if (state.mode === "foreground") {
          return state;
        }

        return {
          ...state,
          mode: "foreground",
          pictureInPictureActive: false,
          pendingReturnNavigation: null,
          shouldReturnToWatchPage: false,
          autoEnterPictureInPicture: false,
          routeExitBypassRequestKey: null,
        };
      }

      return createPlaybackState(action.request);

    case "ROUTE_LEFT":
      if (!state.request) {
        return state;
      }

      if (state.routeExitBypassRequestKey === action.requestKey) {
        return {
          ...state,
          routeExitBypassRequestKey: null,
        };
      }

      if (state.request.requestKey !== action.requestKey) {
        return state;
      }

      if (state.mode !== "foreground" && state.mode !== "post-roll") {
        return state;
      }

      return createEmptyPlaybackState();

    case "EXIT_PLAYBACK":
    case "STOP_PLAYBACK":
      return createEmptyPlaybackState();

    case "MINIMIZE_PLAYBACK":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      return {
        ...state,
        mode: "background-bar",
        pictureInPictureActive: false,
        pendingReturnNavigation: null,
        shouldReturnToWatchPage: false,
        autoEnterPictureInPicture: false,
        routeExitBypassRequestKey: action.requestKey,
      };

    case "RESTORE_PLAYBACK":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      return {
        ...state,
        mode: "foreground",
        pictureInPictureActive: false,
        pendingReturnNavigation: null,
        shouldReturnToWatchPage: false,
        autoEnterPictureInPicture: false,
        routeExitBypassRequestKey: null,
      };

    case "ENTER_PIP":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      return {
        ...state,
        mode: "picture-in-picture",
        pictureInPictureActive: true,
        pendingReturnNavigation: state.pictureInPictureActive
          ? state.pendingReturnNavigation
          : action.requestKey,
        shouldReturnToWatchPage: false,
        autoEnterPictureInPicture: false,
        routeExitBypassRequestKey: null,
      };

    case "LEAVE_PIP":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      if (action.suppressed) {
        return {
          ...state,
          mode: "background-bar",
          pictureInPictureActive: false,
          pendingReturnNavigation: null,
          shouldReturnToWatchPage: false,
          autoEnterPictureInPicture: false,
          routeExitBypassRequestKey: null,
        };
      }

      if (state.mode !== "foreground") {
        if (action.playbackContinues) {
          return {
            ...state,
            mode: "background-bar",
            pictureInPictureActive: false,
            pendingReturnNavigation: null,
            shouldReturnToWatchPage: true,
            autoEnterPictureInPicture: false,
            routeExitBypassRequestKey: null,
          };
        }

        return createEmptyPlaybackState();
      }

      return {
        ...state,
        pictureInPictureActive: false,
        pendingReturnNavigation: null,
        autoEnterPictureInPicture: false,
        routeExitBypassRequestKey: null,
      };

    case "ENTER_POSTROLL":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      if (state.mode !== "foreground") {
        return state;
      }

      return {
        ...state,
        mode: "post-roll",
        autoEnterPictureInPicture: false,
        routeExitBypassRequestKey: null,
      };

    case "REQUEST_RETURN_TO_WATCH":
      if (!state.request || state.request.requestKey !== action.requestKey) {
        return state;
      }

      return {
        ...state,
        pendingReturnNavigation: null,
        shouldReturnToWatchPage: true,
      };

    case "CLEAR_PENDING_RETURN_NAVIGATION":
      if (
        state.request?.requestKey !== action.requestKey ||
        state.pendingReturnNavigation == null
      ) {
        return state;
      }

      return {
        ...state,
        pendingReturnNavigation: null,
      };

    case "UPDATE_SNAPSHOT":
      if (state.request?.requestKey !== action.requestKey) {
        return state;
      }

      if (
        state.snapshot &&
        state.snapshot.currentTime === action.snapshot.currentTime &&
        state.snapshot.duration === action.snapshot.duration &&
        state.snapshot.playing === action.snapshot.playing
      ) {
        return state;
      }

      return {
        ...state,
        snapshot: action.snapshot,
      };

    case "SET_TRANSPORT":
      if (state.request?.requestKey !== action.requestKey || state.transport === action.transport) {
        return state;
      }

      return {
        ...state,
        transport: action.transport,
      };
  }
}
