import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FileVersion } from "@/api/types";
import { useDeleteAudioPreference, useSetAudioPreference } from "@/hooks/queries/trackPreferences";
import { useDeleteSubtitlePreference, useSetSubtitlePreference } from "@/hooks/queries/subtitles";
import type { PrePlaySubtitleSelection } from "@/player/types";

/**
 * A detail-page track selector whose choices are saved on the server.
 *
 * A choice shows at once. When its save fails the selector goes back to the
 * last choice the server kept, unless the viewer has chosen again since: it
 * never shows a choice the server did not keep. The selection belongs to one
 * version's file (track indexes mean something else in another), so another
 * file or title starts over from `initial`, and so does `clear`.
 *
 * `initial` must keep its identity across renders (a module constant): a new
 * one each render would forget the file every time and drop every rollback.
 */
function usePrePlayTrackSelection<S>(contentId: string, fileId: number | undefined, initial: S) {
  const [value, setValue] = useState(initial);
  // Bumped by every choice and reset, so a failure only rolls back the latest.
  const latestChoiceRef = useRef(0);
  // What the server last kept; a failed save returns here, not to an earlier
  // choice that may never have been saved either.
  const savedRef = useRef(initial);
  // Bumped with each file or title change, so a save for the previous one
  // that lands later cannot repopulate savedRef.
  const fileGenerationRef = useRef(0);
  // The choice whose failure put the selector back to savedRef, if it is still
  // showing. A choice refused offline fails at once, ahead of an earlier one
  // still in flight; when that one lands, the selector shows it.
  const rolledBackChoiceRef = useRef(0);

  const [prevContentId, setPrevContentId] = useState(contentId);
  const [prevFileId, setPrevFileId] = useState(fileId);
  if (prevContentId !== contentId || prevFileId !== fileId) {
    setPrevContentId(contentId);
    setPrevFileId(fileId);
    setValue(initial);
  }
  const forgetFile = useCallback(() => {
    latestChoiceRef.current += 1;
    fileGenerationRef.current += 1;
    savedRef.current = initial;
  }, [initial]);
  // A layout effect runs in the same commit as the reset above, before a
  // pending save for the previous file can settle and act on this one.
  useLayoutEffect(forgetFile, [contentId, fileId, forgetFile]);

  // Shows `next`, and runs `save` when the choice is saved at all. `save` uses
  // mutateAsync, not mutate: per-call mutate callbacks only run for the latest
  // call, so an overlapping choice would lose the earlier one's result.
  const choose = (next: S, save?: () => Promise<unknown>) => {
    const choice = ++latestChoiceRef.current;
    const generation = fileGenerationRef.current;
    setValue(next);
    save?.().then(
      () => {
        if (fileGenerationRef.current !== generation) return;
        savedRef.current = next;
        if (rolledBackChoiceRef.current === latestChoiceRef.current) setValue(next);
      },
      () => {
        if (latestChoiceRef.current !== choice) return;
        rolledBackChoiceRef.current = choice;
        setValue(savedRef.current);
      },
    );
  };

  const clear = useCallback(() => {
    forgetFile();
    setValue(initial);
  }, [forgetFile, initial]);

  return { value, choose, clear };
}

type AudioSelectionMode = "auto" | "explicit";
const AUDIO_AUTO: { mode: AudioSelectionMode; index: number | null } = {
  mode: "auto",
  index: null,
};

/**
 * The detail page's audio choice for the selected version.
 *
 * A pick saves that track for the whole series (or the movie) and Auto forgets
 * the saved one, so the next episode and the next visit follow the same rule
 * as this Play.
 *
 * `clear` only resets the selector (another version or title is selected,
 * where track indexes mean something else); it saves nothing.
 */
export function usePrePlayAudioSelection(
  contentId: string,
  prefId: string | undefined,
  version: FileVersion | null | undefined,
) {
  const { value, choose, clear } = usePrePlayTrackSelection(
    contentId,
    version?.file_id,
    AUDIO_AUTO,
  );
  const setAudioPreference = useSetAudioPreference();
  const deleteAudioPreference = useDeleteAudioPreference();

  const select = (trackIndex: number) => {
    const track = version?.audio_tracks?.[trackIndex];
    choose(
      { mode: "explicit", index: trackIndex },
      prefId && track
        ? () => setAudioPreference.mutateAsync({ prefId, trackIndex, track })
        : undefined,
    );
  };

  const reset = () =>
    choose(AUDIO_AUTO, prefId ? () => deleteAudioPreference.mutateAsync(prefId) : undefined);

  return { mode: value.mode, explicitIndex: value.index, select, reset, clear };
}

type SubtitleSelectionMode = "auto" | "off" | "explicit";
const SUBTITLES_AUTO: {
  mode: SubtitleSelectionMode;
  selection: PrePlaySubtitleSelection | null;
} = { mode: "auto", selection: null };

/**
 * The detail page's subtitle choice for the selected version.
 *
 * A pick or Off is saved as the series' (or the movie's) override, the same
 * preference a manual in-player selection saves, so it sticks across visits.
 * Auto clears that override, so the profile's subtitle defaults apply again.
 * Without a `prefId` (an episode with no series) the choice applies to this
 * Play only and nothing is saved.
 *
 * `clear` only resets the selector; it saves nothing.
 */
export function usePrePlaySubtitleSelection(
  contentId: string,
  prefId: string | undefined,
  fileId: number | undefined,
) {
  const { value, choose, clear } = usePrePlayTrackSelection(contentId, fileId, SUBTITLES_AUTO);
  const setSubtitlePreference = useSetSubtitlePreference();
  const deleteSubtitlePreference = useDeleteSubtitlePreference();

  const save = (selection: PrePlaySubtitleSelection | null) =>
    prefId ? () => setSubtitlePreference.mutateAsync({ prefId, selection }) : undefined;

  const select = (selection: PrePlaySubtitleSelection) =>
    choose({ mode: "explicit", selection }, save(selection));
  const selectOff = () => choose({ mode: "off", selection: null }, save(null));
  const reset = () =>
    choose(SUBTITLES_AUTO, prefId ? () => deleteSubtitlePreference.mutateAsync(prefId) : undefined);

  return {
    mode: value.mode,
    explicitSelection: value.selection,
    select,
    selectOff,
    reset,
    clear,
  };
}
