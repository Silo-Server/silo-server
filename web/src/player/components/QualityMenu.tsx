import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Settings } from "lucide-react";
import { resolveActiveQualityOptionId } from "../playback-info";
import type { QualityOption } from "../types";

export interface VersionInfo {
  fileId: number;
  label: string;
  isCurrentSource: boolean;
  isRequestedSource: boolean;
}

interface QualityMenuProps {
  options: QualityOption[];
  activeId: string;
  isTranscoding: boolean;
  error: string | null;
  onSelect: (id: string) => void;
  versions?: VersionInfo[];
  onSwitchVersion?: (fileId: number) => void;
}

export function QualityMenu({
  options,
  activeId,
  isTranscoding,
  error,
  onSelect,
  versions,
  onSwitchVersion,
}: QualityMenuProps) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const handleSelect = useCallback(
    (id: string) => {
      onSelect(id);
      setOpen(false);
    },
    [onSelect],
  );

  // Close on outside click.
  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  // Close on Escape.
  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [open]);

  const menuItemsRef = useRef<(HTMLButtonElement | null)[]>([]);

  const handleMenuKeyDown = useCallback((e: React.KeyboardEvent) => {
    const items = menuItemsRef.current.filter(Boolean) as HTMLButtonElement[];
    if (items.length === 0) return;
    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);
    let nextIndex: number | null = null;

    switch (e.key) {
      case "ArrowDown":
        nextIndex = currentIndex < items.length - 1 ? currentIndex + 1 : 0;
        break;
      case "ArrowUp":
        nextIndex = currentIndex > 0 ? currentIndex - 1 : items.length - 1;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = items.length - 1;
        break;
      case "Escape":
        setOpen(false);
        return;
      default:
        return;
    }
    e.preventDefault();
    items[nextIndex]?.focus();
  }, []);

  if (options.length === 0) return null;

  const resolvedActiveId = resolveActiveQualityOptionId(options, activeId);
  const activeOption = options.find((option) => option.id === resolvedActiveId);
  let menuItemIndex = 0;

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn sm:w-auto sm:gap-1.5 sm:px-3"
        onClick={() => setOpen((v) => !v)}
        aria-label="Quality"
        aria-expanded={open}
        aria-haspopup="menu"
      >
        <Settings className="h-[18px] w-[18px]" />
        <span className="hidden text-[11px] font-medium tracking-wide sm:inline">
          {isTranscoding ? "…" : (activeOption?.label ?? "Quality")}
        </span>
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full z-30 mb-2 min-w-[200px] rounded-lg bg-black/90 py-1 shadow-lg backdrop-blur"
          onKeyDown={handleMenuKeyDown}
        >
          {error && <div className="px-3 py-1 text-xs text-red-400">{error}</div>}
          {/* Version switching (multiple file versions) */}
          {versions && versions.length > 1 && onSwitchVersion && (
            <>
              <div className="px-3 py-1 text-xs tracking-wider text-white/40 uppercase">
                Version
              </div>
              {versions.map((v) => {
                const idx = menuItemIndex++;
                const statusLabels = buildVersionStatusLabels(v);
                return (
                  <button
                    key={v.fileId}
                    ref={(el) => {
                      menuItemsRef.current[idx] = el;
                    }}
                    role="menuitem"
                    type="button"
                    className={`flex w-full px-3 py-2 text-left text-sm hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                      v.isCurrentSource ? "text-white" : "text-white/70"
                    }`}
                    onClick={() => {
                      onSwitchVersion(v.fileId);
                      setOpen(false);
                    }}
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate">{v.label}</span>
                      {statusLabels.length > 0 && (
                        <span className="flex flex-wrap gap-1">
                          {statusLabels.map((status) => (
                            <span
                              key={status}
                              className="rounded border border-white/15 bg-white/10 px-1.5 py-0.5 text-[10px] leading-none text-white/70"
                            >
                              {status}
                            </span>
                          ))}
                        </span>
                      )}
                    </span>
                  </button>
                );
              })}
              <div className="my-1 border-t border-white/10" />
              <div className="px-3 py-1 text-xs tracking-wider text-white/40 uppercase">
                Quality
              </div>
            </>
          )}
          {options.map((opt) => {
            const idx = menuItemIndex++;
            return (
              <button
                key={opt.id}
                ref={(el) => {
                  menuItemsRef.current[idx] = el;
                }}
                role="menuitem"
                type="button"
                className={`flex w-full items-center justify-between px-3 py-2 text-left text-sm hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                  opt.id === resolvedActiveId ? "text-white" : "text-white/70"
                }`}
                aria-current={opt.id === resolvedActiveId ? "true" : undefined}
                onClick={() => handleSelect(opt.id)}
              >
                <span>{opt.label}</span>
                <span className="flex items-center gap-2">
                  <span className="text-xs text-white/40">{opt.sublabel}</span>
                  {opt.id === resolvedActiveId && (
                    <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-white text-black">
                      <Check className="size-3" strokeWidth={3} aria-hidden="true" />
                      <span className="sr-only">Selected</span>
                    </span>
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function buildVersionStatusLabels(version: VersionInfo): string[] {
  if (version.isCurrentSource && version.isRequestedSource) {
    return ["Playing"];
  }

  const labels: string[] = [];
  if (version.isCurrentSource) {
    labels.push("Playing");
  }
  if (version.isRequestedSource) {
    labels.push("Requested");
  }
  return labels;
}
