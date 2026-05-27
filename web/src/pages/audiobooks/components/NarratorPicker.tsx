import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { ChevronDown } from "lucide-react";
import type { AudiobookNarration } from "@/lib/audiobooks/types";

interface NarratorPickerProps {
  currentNarrator: string;
  currentContentId: string;
  others: AudiobookNarration[];
}

export function NarratorPicker({ currentNarrator, currentContentId, others }: NarratorPickerProps) {
  const [open, setOpen] = useState(false);
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (!wrapperRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, [open]);

  const options: AudiobookNarration[] = [
    { content_id: currentContentId, title: "", narrator: currentNarrator },
    ...others,
  ];

  return (
    <span ref={wrapperRef} className="relative inline-block">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="hover:text-foreground inline-flex items-center gap-1 rounded border border-transparent px-1 py-0.5 transition-colors hover:border-current/30"
      >
        {currentNarrator}
        <ChevronDown className="h-3 w-3" />
      </button>
      {open && (
        <div
          role="listbox"
          className="bg-popover absolute top-full left-0 z-50 mt-1 min-w-[220px] overflow-hidden rounded-md border shadow-lg"
        >
          <div className="text-muted-foreground px-3 py-2 text-[11px] tracking-[0.12em] uppercase">
            {options.length} narrations
          </div>
          {options.map((it) => {
            const isCurrent = it.content_id === currentContentId;
            return (
              <button
                key={it.content_id}
                role="option"
                aria-selected={isCurrent}
                type="button"
                onClick={() => {
                  setOpen(false);
                  if (!isCurrent) navigate(`/audiobooks/book/${it.content_id}`);
                }}
                className={`hover:bg-muted/60 flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm ${
                  isCurrent ? "bg-muted/40 font-medium" : ""
                }`}
              >
                <span className="truncate">{it.narrator || "Unknown narrator"}</span>
                {it.year ? (
                  <span className="text-muted-foreground shrink-0 text-xs">{it.year}</span>
                ) : null}
              </button>
            );
          })}
        </div>
      )}
    </span>
  );
}
