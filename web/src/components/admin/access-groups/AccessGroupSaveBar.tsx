import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

interface AccessGroupSaveBarProps {
  onDiscard: () => void;
  onSave: () => void;
  saveDisabled: boolean;
  discardDisabled: boolean;
  saveLabel: string;
  /** Messages about the save (errors, a conflict and its reload), shown above the buttons. */
  messages?: ReactNode;
}

/**
 * The access group editor's save bar on a phone: fixed along the bottom of the
 * viewport, above the safe area and any playback bar. It leaves an in-flow
 * spacer of its own height so the last control can scroll clear of it, and
 * sets the page's bottom scroll padding to match so a focused field is not
 * scrolled in behind it. It holds no focus of its own.
 */
export function AccessGroupSaveBar({
  onDiscard,
  onSave,
  saveDisabled,
  discardDisabled,
  saveLabel,
  messages,
}: AccessGroupSaveBarProps) {
  const t = useT();
  const bar = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);

  useLayoutEffect(() => {
    const element = bar.current;
    if (!element) return;
    const measure = () => setHeight(element.getBoundingClientRect().height);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useLayoutEffect(() => {
    const root = document.documentElement;
    const previous = root.style.scrollPaddingBottom;
    root.style.scrollPaddingBottom = `calc(${height}px + var(--playback-bar-clearance, 0px))`;
    return () => {
      root.style.scrollPaddingBottom = previous;
    };
  }, [height]);

  return (
    <>
      <div aria-hidden="true" style={{ height }} />
      <div
        ref={bar}
        role="region"
        aria-label={t("web.shell.saveBar.regionLabel")}
        className="bg-background border-border fixed inset-x-0 bottom-(--playback-bar-clearance,0px) z-40 space-y-2 border-t px-4 pt-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))]"
      >
        {messages ? (
          <div className="max-h-[40dvh] space-y-2 overflow-y-auto text-sm break-words">
            {messages}
          </div>
        ) : null}
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            className="h-11"
            onClick={onDiscard}
            disabled={discardDisabled}
          >
            {t("web.shell.unsavedChanges.discard")}
          </Button>
          <Button
            type="button"
            className="h-11 min-w-0 flex-1"
            onClick={onSave}
            disabled={saveDisabled}
          >
            {saveLabel}
          </Button>
        </div>
      </div>
    </>
  );
}
