import {
  onlineManager,
  useMutation,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";

import { t } from "@/i18n";

import {
  captureProfileRequestContext,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { VersionAudioTrack } from "@/api/types";
import { v2 } from "@/api/v2/request";
import { buildAudioPreferenceBody } from "@/player/utils/audioChoicePersistence";
import {
  runTrackPreferenceWrite,
  type TrackPreferenceDimension,
} from "@/player/utils/trackPreferenceWrites";

import { itemKeys } from "./keys";

/**
 * Sent even offline, so it fails at once: replayed on reconnect, a track
 * choice would replace whatever another browser chose for the title meanwhile.
 */
export const TRACK_PREFERENCE_WRITE_NETWORK_MODE = "always";

// Track preferences feed the effective defaults on item details; a
// series-keyed preference feeds every episode's detail, so invalidate broadly.
export function invalidateItemDetails(queryClient: QueryClient): Promise<unknown> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: itemKeys.details() }),
    queryClient.invalidateQueries({ queryKey: ["catalog", "items"] }),
  ]);
}

/**
 * Runs a track preference write for the acting profile after its earlier
 * writes for the same title and dimension, so the viewer's last choice is the
 * one that sticks. The profile is captured now: a write that waits in line
 * still goes out for the profile that made the choice.
 *
 * A choice made offline fails here, as its request would, instead of waiting
 * in line: an earlier request can stay pending until the browser reconnects,
 * and the choice would then replace a newer one made in another browser.
 */
export function runOrderedPreferenceWrite<T>(
  dimension: TrackPreferenceDimension,
  prefId: string,
  write: (profileContext: ProfileRequestContextSnapshot) => Promise<T>,
): Promise<T> {
  if (!onlineManager.isOnline()) return Promise.reject(new TypeError("Failed to fetch"));
  const profileContext = captureProfileRequestContext();
  if (!profileContext) return Promise.reject(new StaleApiRequestContextError());
  return runTrackPreferenceWrite(
    { profileId: profileContext.profileId, dimension, targetId: prefId },
    () => write(profileContext),
  );
}

interface SetAudioPreferenceInput {
  /** Movie content ID or episode series ID (preferences are series-scoped). */
  prefId: string;
  /** The track's index in its file's audio list. */
  trackIndex: number;
  track: VersionAudioTrack;
}

/**
 * Saves a manual audio pick as the series' audio choice, so every episode of
 * the series (or the movie) plays that track until the viewer picks Auto.
 * Playback never saves one on its own.
 */
export function useSetAudioPreference() {
  const queryClient = useQueryClient();

  return useMutation({
    networkMode: TRACK_PREFERENCE_WRITE_NETWORK_MODE,
    mutationFn: ({ prefId, trackIndex, track }: SetAudioPreferenceInput) =>
      runOrderedPreferenceWrite("audio", prefId, (profileContext) =>
        v2("PUT /api/v2/audio-prefs/{series_id}", {
          path: { series_id: prefId },
          body: buildAudioPreferenceBody(trackIndex, track),
          profileContext,
        }),
      ),
    onSuccess: () => invalidateItemDetails(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.player.tracks.saveAudioFailed"));
    },
  });
}

/**
 * Forgets the series' audio choice so playback follows the profile's audio
 * defaults again. The server also clears the series-scope audio language
 * setting, so nothing else needs deleting here.
 */
export function useDeleteAudioPreference() {
  const queryClient = useQueryClient();

  return useMutation({
    networkMode: TRACK_PREFERENCE_WRITE_NETWORK_MODE,
    mutationFn: (prefId: string) =>
      runOrderedPreferenceWrite("audio", prefId, (profileContext) =>
        v2("DELETE /api/v2/audio-prefs/{series_id}", {
          path: { series_id: prefId },
          profileContext,
        }),
      ),
    onSuccess: () => invalidateItemDetails(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.player.tracks.resetAudioFailed"));
    },
  });
}
