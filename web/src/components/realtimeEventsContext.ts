import { createContext, useContext, useEffect, useMemo } from "react";
import type { AdminJob, EventChannel } from "@/api/types";

export type RealtimeConnectionState = "connecting" | "live" | "disconnected";

export interface EventChannelHandlers {
  onSnapshot?: (message: unknown) => void;
  onEvent?: (message: unknown) => void;
}

export interface RealtimeEventsActionsContextValue {
  awaitAdminJob: (jobId: string) => Promise<AdminJob>;
  subscribeChannel: (channel: EventChannel, handlers?: EventChannelHandlers) => () => void;
}

export interface RealtimeEventsContextValue extends RealtimeEventsActionsContextValue {
  connectionState: RealtimeConnectionState;
}

export const RealtimeConnectionStateContext =
  createContext<RealtimeConnectionState>("disconnected");

export const RealtimeEventsActionsContext = createContext<RealtimeEventsActionsContextValue>({
  awaitAdminJob: async () => {
    throw new Error("Realtime events provider is not mounted");
  },
  subscribeChannel: () => () => {},
});

export function useRealtimeConnectionState() {
  return useContext(RealtimeConnectionStateContext);
}

export function useRealtimeEventsActions() {
  return useContext(RealtimeEventsActionsContext);
}

export function useRealtimeEvents(): RealtimeEventsContextValue {
  const connectionState = useRealtimeConnectionState();
  const { awaitAdminJob, subscribeChannel } = useRealtimeEventsActions();

  return useMemo(
    () => ({
      connectionState,
      awaitAdminJob,
      subscribeChannel,
    }),
    [awaitAdminJob, connectionState, subscribeChannel],
  );
}

export function useEventChannel(channel: EventChannel, handlers?: EventChannelHandlers) {
  const { subscribeChannel } = useRealtimeEventsActions();

  useEffect(() => subscribeChannel(channel, handlers), [channel, handlers, subscribeChannel]);
}
