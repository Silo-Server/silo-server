import { ChevronLeft } from "lucide-react";
import { type To } from "react-router";

import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useT } from "@/i18n";
import { hasEarlierEntry } from "@/lib/navigationHistory";

interface PageBackProps {
  /** Accessible name; defaults to "Go back". */
  label?: string;
  to?: To;
  /**
   * `to` is this page's ancestor rather than a bare fallback: go there rather
   * than stepping back one, and play the motion backwards. Use it wherever the
   * destination is known — a season's series, a wizard's list — and leave it
   * off on detail pages, where the page behind is whatever the user was
   * browsing and `to` only covers a cold entry.
   */
  up?: boolean;
  /**
   * When true, pins the button to the viewport on lg+ so it stays visible
   * while scrolling. The offset matches the app sidebar (260px) so the
   * button sits just inside the page content area.
   */
  floating?: boolean;
}

export default function PageBack({ label, to = "/", up = false, floating = false }: PageBackProps) {
  const t = useT();
  const navigate = useViewTransitionNavigate();
  const position = floating
    ? "absolute top-4 start-2 sm:top-6 lg:fixed lg:start-[268px]"
    : "absolute top-4 start-2 sm:top-6";

  function goBack() {
    // With nothing behind us — a cold entry, a shared link — there is no step
    // to take, so `to` becomes the destination and still reads as going up.
    if (up || !hasEarlierEntry()) {
      navigate(to, { up: true });
      return;
    }

    navigate(-1);
  }

  return (
    <button
      type="button"
      aria-label={label ?? t("web.shell.pageBack.goBack")}
      onClick={goBack}
      className={`glass glass-hover glass-hover-accent text-foreground ${position} z-20 flex items-center justify-center rounded-full p-1.5 shadow-md`}
    >
      <ChevronLeft className="size-5" />
    </button>
  );
}
