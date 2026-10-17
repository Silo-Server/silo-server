import { useEffect, useRef } from "react";

export function BufferingQualityPill({
  kind,
  remainingMs,
  keyboard,
  answer,
}: {
  kind: "prompt" | "undo";
  remainingMs: number;
  keyboard: boolean;
  answer: (event: string) => void;
}) {
  const primary = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (
      keyboard &&
      !active?.closest(
        'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"], [role="listbox"], [role="slider"]',
      )
    )
      primary.current?.focus();
  }, [kind, keyboard]);
  return (
    <div
      role="status"
      data-buffering-prompt="true"
      className="absolute bottom-28 left-1/2 z-50 w-max max-w-[90%] -translate-x-1/2 rounded-xl bg-black/90 p-4 text-white"
    >
      <p>{kind === "undo" ? "Quality lowered to stop buffering" : "Playback keeps buffering"}</p>
      <button
        ref={primary}
        className="mt-2 rounded bg-white px-3 py-2 text-black"
        onClick={() => answer(kind === "undo" ? "select_restore" : "select_lower")}
      >
        {kind === "undo" ? "Restore quality" : "Lower quality"}
      </button>
      {kind === "prompt" && (
        <button
          data-buffering-keep="true"
          className="ml-3 px-3 py-2"
          onClick={() => answer("select_keep")}
        >
          Keep quality
        </button>
      )}
      <progress
        aria-label="Time remaining"
        className="mt-2 block h-1 w-full"
        max={15000}
        value={remainingMs}
      />
    </div>
  );
}
