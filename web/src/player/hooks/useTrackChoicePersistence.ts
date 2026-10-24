import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { invalidateItemDetails } from "@/hooks/queries/trackPreferences";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import type { SubtitleInventoryItemV3 } from "../protocol-v3";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";
import { buildAudioPreferenceBody, sendAudioChoiceRequest } from "../utils/audioChoicePersistence";
import {
  buildSubtitleChoiceRequests,
  sendSubtitleChoiceRequest,
} from "../utils/subtitleChoicePersistence";
import {
  runPlayerTrackPreferenceWrite,
  StaleTrackPreferenceWriteError,
} from "../utils/trackPreferenceWrites";

/**
 * Saves a viewer's in-player track picks for the whole series (or the movie,
 * keyed by its content id). Only the player menus call these, so restores,
 * reconnects, and server replans never save a choice. Indexes belong to the
 * file playing, so each pick is described by that file's tracks.
 */
export function useTrackChoicePersistence({
  seriesId,
  audioTracks,
  subtitleTracks,
}: {
  seriesId: string;
  audioTracks: readonly PlayerAudioTrack[];
  subtitleTracks: readonly PlayerSubtitleInfo[];
}) {
  const config = usePlayerConfig();
  const queryClient = useQueryClient();

  const saveAudioChoice = useCallback(
    (index: number) => {
      const track = audioTracks[index];
      if (!track) return;
      sendAudioChoiceRequest(config, seriesId, buildAudioPreferenceBody(index, track)).then(
        () => invalidateItemDetails(queryClient),
        (err: unknown) => {
          // The viewer switched profile before it went out; it isn't theirs.
          if (err instanceof StaleTrackPreferenceWriteError) return;
          toast.error(err instanceof Error ? err.message : "Failed to save audio preference");
        },
      );
    },
    [audioTracks, config, queryClient, seriesId],
  );

  /**
   * buildSubtitleChoiceRequests decides what a pick is worth storing and
   * where; this only issues the requests. They are independent on purpose: a
   * failed settings write must not cost the user the track they picked, and a
   * failed track write must not cost them the language, so each is best effort
   * on its own rather than one composite request that half-applies.
   */
  const saveSubtitleChoice = useCallback(
    (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => {
      const requests = buildSubtitleChoiceRequests({
        seriesId,
        index,
        tracks: subtitleTracks,
        inventoryTrack,
      });
      if (requests.length === 0) return;
      // Best effort, but after any earlier pick for the series has landed.
      void runPlayerTrackPreferenceWrite(config, "subtitle", seriesId, (ownerConfig) =>
        Promise.allSettled(
          requests.map((request) => sendSubtitleChoiceRequest(ownerConfig, request)),
        ),
      ).catch(() => {
        // Best effort; a write for a profile no longer active is dropped.
      });
    },
    [config, seriesId, subtitleTracks],
  );

  return { saveAudioChoice, saveSubtitleChoice };
}
