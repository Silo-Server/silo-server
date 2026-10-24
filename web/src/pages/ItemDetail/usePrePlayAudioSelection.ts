import { useCallback, useEffect, useRef, useState } from "react";
import type { FileVersion } from "@/api/types";
import { useDeleteAudioPreference, useSetAudioPreference } from "@/hooks/queries/trackPreferences";

type AudioSelectionMode = "auto" | "explicit";

/**
 * The detail page's audio choice for the selected version.
 *
 * A pick saves that track for the whole series (or the movie) and Auto forgets
 * the saved one, so the next episode and the next visit follow the same rule
 * as this Play. When a write fails the selector goes back to the last choice
 * the server kept, unless the viewer has chosen again since: it never shows a
 * choice the server did not keep.
 *
 * `clear` only resets the selector (another version or title is selected,
 * where track indexes mean something else); it saves nothing.
 */
export function usePrePlayAudioSelection(
  contentId: string,
  prefId: string | undefined,
  version: FileVersion | null | undefined,
) {
  const [mode, setMode] = useState<AudioSelectionMode>("auto");
  const [explicitIndex, setExplicitIndex] = useState<number | null>(null);
  const setAudioPreference = useSetAudioPreference();
  const deleteAudioPreference = useDeleteAudioPreference();
  // Bumped by every choice and reset, so a failure only rolls back the latest.
  const latestChoiceRef = useRef(0);
  // The selector state the server last kept; a failed write returns here, not
  // to an earlier choice that may never have been saved either. Its index
  // belongs to one version's file, so another version or title starts over.
  const savedRef = useRef<{ mode: AudioSelectionMode; index: number | null }>({
    mode: "auto",
    index: null,
  });
  // Bumped with each version or title change, so a write for the previous one
  // that lands later cannot repopulate savedRef.
  const fileGenerationRef = useRef(0);

  const [prevContentId, setPrevContentId] = useState(contentId);
  if (prevContentId !== contentId) {
    setPrevContentId(contentId);
    setMode("auto");
    setExplicitIndex(null);
  }
  const forgetFile = useCallback(() => {
    latestChoiceRef.current += 1;
    fileGenerationRef.current += 1;
    savedRef.current = { mode: "auto", index: null };
  }, []);
  useEffect(forgetFile, [contentId, forgetFile]);

  const show = useCallback((nextMode: AudioSelectionMode, nextIndex: number | null) => {
    setMode(nextMode);
    setExplicitIndex(nextIndex);
  }, []);

  // Returns the write's callbacks: success records what the server now keeps,
  // failure shows that again, unless the viewer has chosen since.
  const track = (nextMode: AudioSelectionMode, nextIndex: number | null) => {
    const choice = ++latestChoiceRef.current;
    const generation = fileGenerationRef.current;
    return {
      onSuccess: () => {
        if (fileGenerationRef.current === generation) {
          savedRef.current = { mode: nextMode, index: nextIndex };
        }
      },
      onError: () => {
        if (latestChoiceRef.current === choice) show(savedRef.current.mode, savedRef.current.index);
      },
    };
  };

  const select = (trackIndex: number) => {
    const audioTrack = version?.audio_tracks?.[trackIndex];
    const callbacks = track("explicit", trackIndex);
    show("explicit", trackIndex);
    if (!prefId || !audioTrack) return;
    // mutateAsync, not mutate: per-call mutate callbacks only run for the
    // latest call, so an overlapping pick would lose the earlier one's result.
    setAudioPreference
      .mutateAsync({ prefId, trackIndex, track: audioTrack })
      .then(callbacks.onSuccess, callbacks.onError);
  };

  const reset = () => {
    const callbacks = track("auto", null);
    show("auto", null);
    if (!prefId) return;
    deleteAudioPreference.mutateAsync(prefId).then(callbacks.onSuccess, callbacks.onError);
  };

  const clear = useCallback(() => {
    forgetFile();
    show("auto", null);
  }, [forgetFile, show]);

  return { mode, explicitIndex, select, reset, clear };
}
