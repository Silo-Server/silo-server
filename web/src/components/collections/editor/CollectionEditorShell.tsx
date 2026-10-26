import type { ReactNode } from "react";

/**
 * The editor page's layout, the same for every type and scope and at every
 * width: one column, at most 768px wide, in the order a collection is made.
 * The header, any banner, then the cards (`children`: name, contents, where
 * it shows, look) and the save bar.
 */
export function CollectionEditorShell({
  header,
  banner,
  children,
  footer,
}: {
  header: ReactNode;
  banner?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="page-shell py-4 sm:py-6">
      <div
        data-testid="editor-column"
        className="mx-auto grid max-w-3xl grid-cols-[minmax(0,1fr)] gap-4"
      >
        <div className="mb-2">{header}</div>
        {banner}
        {children}
        {footer}
      </div>
    </div>
  );
}
