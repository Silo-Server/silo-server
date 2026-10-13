/* eslint-disable react-refresh/only-export-components -- the provider, its hook, and the card ids belong together. */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";

export type AccessCardId = "signin" | "library" | "downloads" | "playback" | "requests";

export const ACCESS_CARD_TITLES: Record<AccessCardId, string> = {
  signin: "Sign-in & role",
  library: "Library access",
  downloads: "Downloads",
  playback: "Playback & streaming",
  requests: "Requests",
};

/** The one card being edited, with what the unsaved-changes prompt needs from it. */
export interface ActiveCardEdit {
  id: AccessCardId;
  changeCount: number;
  save: () => Promise<boolean>;
  discard: () => void;
}

interface CardEditing {
  active: ActiveCardEdit | null;
  /** Starts editing a card; false while another card is being edited. */
  begin(edit: ActiveCardEdit): boolean;
  update(id: AccessCardId, changeCount: number): void;
  end(id: AccessCardId): void;
}

const CardEditingContext = createContext<CardEditing | null>(null);

/**
 * Lets one Access & limits card edit at a time and tells the page's guards
 * whether that card holds unsaved changes.
 */
export function CardEditingProvider({ children }: { children: ReactNode }) {
  const [active, setActive] = useState<ActiveCardEdit | null>(null);
  // Read synchronously so two cards starting in the same tick cannot both win.
  const current = useRef<ActiveCardEdit | null>(null);

  const begin = useCallback((edit: ActiveCardEdit) => {
    if (current.current && current.current.id !== edit.id) return false;
    current.current = edit;
    setActive(edit);
    return true;
  }, []);
  const update = useCallback((id: AccessCardId, changeCount: number) => {
    const edit = current.current;
    if (!edit || edit.id !== id || edit.changeCount === changeCount) return;
    current.current = { ...edit, changeCount };
    setActive(current.current);
  }, []);
  const end = useCallback((id: AccessCardId) => {
    if (current.current?.id !== id) return;
    current.current = null;
    setActive(null);
  }, []);

  // Reload and tab close never reach the router; this arms the browser's prompt.
  useReportUnsavedChanges((active?.changeCount ?? 0) > 0);

  const value = useMemo(() => ({ active, begin, update, end }), [active, begin, update, end]);
  return <CardEditingContext.Provider value={value}>{children}</CardEditingContext.Provider>;
}

const NO_PROVIDER: CardEditing = {
  active: null,
  begin: () => true,
  update: () => {},
  end: () => {},
};

/** The page's card-editing state; outside a provider every card may edit on its own. */
export function useCardEditing(): CardEditing {
  return useContext(CardEditingContext) ?? NO_PROVIDER;
}
