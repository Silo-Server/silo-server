import type { ReactNode } from "react";

interface DetailLayoutProps {
  /** The page's hero: a DetailHero, or a wrapper that holds one. */
  hero: ReactNode;
  /** Sections below the hero, stacked on the page shell. */
  children?: ReactNode;
  /** Rendered after the sections, outside the shell: dialogs and sheets. */
  overlays?: ReactNode;
}

/**
 * The movie and series detail layout: the hero, then the supporting sections
 * on the page shell. Library items and titles known only from TMDB both render
 * through it, so a title outside the library reads like one inside it.
 */
export default function DetailLayout({ hero, children, overlays }: DetailLayoutProps) {
  return (
    <div>
      {hero}
      <div className="page-shell detail-supporting-content space-y-12 py-10 sm:space-y-14">
        {children}
      </div>
      {overlays}
    </div>
  );
}

/** A supporting section under the detail hero, with the shared heading. */
export function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <h2 className="mb-5 text-xl font-semibold tracking-tight">{title}</h2>
      {children}
    </div>
  );
}
