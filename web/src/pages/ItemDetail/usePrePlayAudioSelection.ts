import { useCallback, useEffect, useRef, useState } from "react";
import type { FileVersion } from "@/api/types";
import { useDeleteAudioPreference, useSetAudioPreference } from "@/hooks/queries/trackPreferences";

type AudioSelectionMode = "auto" | "explicit";

/**
 * The detail page's audio choice for the selected version.
 *
 * A pick saves that track for the whole series (or the movie) and Auto forgets
 * the saved one, so the next episode and the next visit follow the same rule
 * as this Play. When a write fails the selector goes back to what it showed
 * before, unless the viewer has chosen again since: it never shows a choice
 * the server did not keep.
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

  const [prevContentId, setPrevContentId] = useState(contentId);
  if (prevContentId !== contentId) {
    setPrevContentId(contentId);
    setMode("auto");
    setExplicitIndex(null);
  }
  useEffect(() => {
    latestChoiceRef.current += 1;
  }, [contentId]);

  const show = useCallback((nextMode: AudioSelectionMode, nextIndex: number | null) => {
    setMode(nextMode);
    setExplicitIndex(nextIndex);
  }, []);

  const rollbackTo = (previousMode: AudioSelectionMode, previousIndex: number | null) => {
    const choice = ++latestChoiceRef.current;
    return () => {
      if (latestChoiceRef.current === choice) show(previousMode, previousIndex);
    };
  };

  const select = (trackIndex: number) => {
    const track = version?.audio_tracks?.[trackIndex];
    const rollback = rollbackTo(mode, explicitIndex);
    show("explicit", trackIndex);
    if (!prefId || !track) return;
    setAudioPreference.mutate({ prefId, trackIndex, track }, { onError: rollback });
  };

  const reset = () => {
    const rollback = rollbackTo(mode, explicitIndex);
    show("auto", null);
    if (!prefId) return;
    deleteAudioPreference.mutate(prefId, { onError: rollback });
  };

  const clear = useCallback(() => {
    latestChoiceRef.current += 1;
    show("auto", null);
  }, [show]);

  return { mode, explicitIndex, select, reset, clear };
}
