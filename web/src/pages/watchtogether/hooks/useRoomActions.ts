import { useCallback, useState } from "react";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { WatchTogetherRoomConnectionResult } from "@/player/hooks/useWatchTogetherRoomConnection";
import type {
  SelectWatchTogetherRoomItemInput,
  WatchTogetherSelectionMode,
} from "@/lib/watchTogether";

/**
 * Toast-wrapped lobby actions. Every action captures authority before the
 * call and only reports under that same authority, so feedback never lands
 * in a replaced profile's session. Errors that mean "you were replaced" are
 * silent; everything else is a toast.
 */
export function useRoomActions(connection: WatchTogetherRoomConnectionResult) {
  const [busy, setBusy] = useState<null | "stage" | "start" | "stop" | "mode" | "promote">(null);

  const run = useCallback(
    async <T>(
      kind: NonNullable<typeof busy>,
      action: () => Promise<T>,
      success?: (value: T) => string | null,
      failure = "Something went wrong",
    ): Promise<T | null> => {
      const authority = captureProfileRequestContext();
      setBusy(kind);
      try {
        const value = await action();
        if (authority && isCapturedProfileAuthorityActive(authority)) {
          const message = success?.(value);
          if (message) toast.success(message);
        }
        return value;
      } catch (error) {
        if (error instanceof StaleApiRequestContextError) return null;
        if (authority && isCapturedProfileAuthorityActive(authority))
          toast.error(error instanceof Error ? error.message : failure);
        return null;
      } finally {
        setBusy((current) => (current === kind ? null : current));
      }
    },
    [],
  );

  const stage = useCallback(
    (input: SelectWatchTogetherRoomItemInput) =>
      run(
        "stage",
        () => connection.stageItem(input),
        () => null,
        "Could not stage that",
      ),
    [connection, run],
  );

  const start = useCallback(
    () =>
      run(
        "start",
        () => connection.startPlayback(),
        (room) => (room?.phase === "playing" ? "Starting for everyone" : null),
        "Could not start playback",
      ),
    [connection, run],
  );

  const stop = useCallback(
    () =>
      run(
        "stop",
        () => connection.stopPlayback(),
        (room) => (room?.phase === "lobby" ? "Stopped for everyone. Pick what's next." : null),
        "Could not stop playback",
      ),
    [connection, run],
  );

  const switchMode = useCallback(
    (mode: WatchTogetherSelectionMode) =>
      run(
        "mode",
        () => connection.updateSelectionMode(mode),
        (room) =>
          room ? (room.selection_mode === "vote" ? "The room votes now" : "You pick now") : null,
        "Could not change how the room picks",
      ),
    [connection, run],
  );

  const promote = useCallback(
    (suggestionId: string) =>
      run(
        "promote",
        () => connection.promoteSuggestion(suggestionId),
        (room) => (room ? "Starting for everyone" : null),
        "Could not start that suggestion",
      ),
    [connection, run],
  );

  const setReady = useCallback(
    (ready: boolean) => {
      const sent = connection.setLobbyReady(ready);
      if (!sent.ok) toast.error("Not connected to the room yet");
      return sent.ok;
    },
    [connection],
  );

  return { busy, stage, start, stop, switchMode, promote, setReady };
}
