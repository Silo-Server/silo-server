import { useCallback, useLayoutEffect, useRef, useState } from "react";

type Save<T> = (next: T) => Promise<unknown>;

interface Machine<T> {
  /** Shares this machine with a later editor for the same value while it saves. */
  key: string | undefined;
  /** The value the server last acknowledged. */
  confirmed: T;
  /** Patch whose save is in flight. */
  sending: Partial<T> | null;
  /** Edits made while a save was in flight, merged, with the save to send them. */
  queued: Partial<T> | null;
  queuedSave: Save<T> | null;
  /** Shows the machine's state in the editor mounted on it, if any. */
  publish: ((m: Machine<T>) => void) | null;
}

/**
 * Keyed machines with a save in flight. An editor closed mid-save leaves its
 * save running; one reopened on the same key joins it instead of starting from
 * a stored copy the save hasn't reached yet and racing it with a second queue.
 */
const inFlight = new Map<string, unknown>();

/** Send one patch; when it settles, send whatever queued up meanwhile. */
function send<T>(m: Machine<T>, patch: Partial<T>, save: Save<T>) {
  const next = { ...m.confirmed, ...patch };
  m.sending = patch;
  if (m.key !== undefined) inFlight.set(m.key, m);
  void save(next)
    .then(
      () => {
        m.confirmed = next;
      },
      () => undefined,
    )
    .finally(() => {
      m.sending = null;
      const { queued, queuedSave } = m;
      m.queued = null;
      m.queuedSave = null;
      if (queued && queuedSave) send(m, queued, queuedSave);
      else if (m.key !== undefined && inFlight.get(m.key) === m) inFlight.delete(m.key);
      m.publish?.(m);
    });
}

const viewOf = <T>(m: Machine<T>) => ({
  value: { ...m.confirmed, ...m.sending, ...m.queued } as T,
  saving: m.sending !== null,
});

/**
 * Local draft for an editor that saves every change as it is made (a panel
 * with no Save button), so the draft never claims a value the server didn't
 * store.
 *
 * Each edit shows at once. Saves go out one at a time: edits made while one is
 * in flight merge into a single queued patch. Every request is built when it
 * is sent, from the last acknowledged value plus that patch, so a rejected edit
 * is dropped instead of riding along in the next save, and two requests never
 * race at the server. A failed save puts its fields back to the last
 * acknowledged value without undoing newer edits; callers report the failure
 * themselves (the save function may toast and rethrow).
 *
 * Unmounting drops edits that haven't been sent, so nothing goes out after
 * the editor is gone (where a reset or a reopened editor couldn't stop it);
 * a save already in flight still completes. Call `discardQueued` before
 * clearing the stored value while the editor is mounted.
 *
 * With a `key` naming the stored value, an editor opened while an earlier
 * one's save for that key is still in flight starts from that save, not from
 * `initial`, and queues behind it. The caller's save should settle only once
 * the copy `initial` is read from shows the saved value.
 */
export function useAutosavedDraft<T extends object>(initial: T, key?: string) {
  const [joined] = useState<Machine<T>>(
    () =>
      (key !== undefined ? (inFlight.get(key) as Machine<T> | undefined) : undefined) ?? {
        key,
        confirmed: initial,
        sending: null,
        queued: null,
        queuedSave: null,
        publish: null,
      },
  );
  const machine = useRef(joined);
  const [view, setView] = useState(() => viewOf(joined));

  const publish = useCallback((m: Machine<T>) => setView(viewOf(m)), []);

  // Layout cleanup runs inside the commit that removes the editor, so a save
  // settling before passive effects flush (a transition unmount) can't start
  // the queued one.
  useLayoutEffect(() => {
    const m = machine.current;
    m.publish = publish;
    return () => {
      if (m.publish !== publish) return;
      m.publish = null;
      m.queued = null;
      m.queuedSave = null;
    };
  }, [publish]);

  const edit = useCallback(
    (patch: Partial<T>, save: Save<T>) => {
      const m = machine.current;
      if (m.sending) {
        m.queued = { ...m.queued, ...patch };
        m.queuedSave = save;
      } else {
        send(m, patch, save);
      }
      publish(m);
    },
    [publish],
  );

  /** Start from a freshly read server value; pending edits stay on top of it. */
  const reset = useCallback(
    (value: T) => {
      const m = machine.current;
      m.confirmed = value;
      publish(m);
    },
    [publish],
  );

  /**
   * Drop edits not sent yet, for when the stored value is about to be cleared:
   * a queued save would otherwise land after the clear and write it back.
   */
  const discardQueued = useCallback(() => {
    const m = machine.current;
    m.queued = null;
    m.queuedSave = null;
    publish(m);
  }, [publish]);

  return { value: view.value, saving: view.saving, edit, reset, discardQueued };
}
