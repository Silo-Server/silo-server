import { useCallback, useState } from "react";

export interface StagedDraft<B, D> {
  /** The record the draft was read from, validator included; undefined until loaded. */
  base: B | undefined;
  draft: D | undefined;
  /** How many staged edits differ from `base`; 0 while clean or unloaded. */
  changes: number;
  update: (change: (draft: D) => D) => void;
  /** Throws the edits away and goes back to `base`. */
  reset: () => void;
  /** Starts over from a newer record: after a save, or an explicit reload. */
  adopt: (base: B) => void;
}

/**
 * A staged edit of one server record that is saved through the page's save
 * bar rather than on its own. While the draft is clean it follows the query
 * to a newer revision (a different validator), so a background refresh shows
 * the latest record; once it has edits it holds on to the record and the
 * validator they were made against, so a refresh can neither overwrite them
 * nor let a later save skip a conflict.
 *
 * A save must write the record it got back into the query cache before it
 * `adopt`s it; otherwise the clean draft would follow the query back to the
 * revision the save replaced.
 */
export function useStagedDraft<B extends { etag?: string }, D>(
  source: B | undefined,
  toDraft: (base: B) => D,
  countChanges: (draft: D, base: D) => number,
): StagedDraft<B, D> {
  const [state, setState] = useState<{ base: B; draft: D } | null>(null);

  let current = state;
  const held = state !== null && countChanges(state.draft, toDraft(state.base)) > 0;
  if (source !== undefined && (state === null || (state.base.etag !== source.etag && !held))) {
    current = { base: source, draft: toDraft(source) };
    // Adjusting state during render (rather than in an effect) keeps the
    // first paint of a new record free of the stale one.
    setState(current);
  }

  const update = useCallback((change: (draft: D) => D) => {
    setState((prev) => (prev ? { ...prev, draft: change(prev.draft) } : prev));
  }, []);
  const reset = useCallback(() => {
    setState((prev) => (prev ? { base: prev.base, draft: toDraft(prev.base) } : prev));
    // toDraft is a module-level function at every call site.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const adopt = useCallback((base: B) => {
    setState({ base, draft: toDraft(base) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    base: current?.base,
    draft: current?.draft,
    changes: current ? countChanges(current.draft, toDraft(current.base)) : 0,
    update,
    reset,
    adopt,
  };
}
