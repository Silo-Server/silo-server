import { OVERFLOW_CLIP, useOverflowsInline } from "@/hooks/useOverflowsInline";
import { cn } from "@/lib/utils";

/**
 * Folder paths, one per line. They read left to right in any language, and each line,
 * a short path's or a wrapped one's, sits on the page's start side. A folder name that
 * can't wrap, such as one long run of joined emoji, is clipped at the list's end edge
 * while it overflows, so it keeps its start and the list keeps its width.
 */
export function PathList({ paths, className }: { paths: readonly string[]; className?: string }) {
  const [ref, overflows] = useOverflowsInline<HTMLUListElement>(paths.join("\n"));
  return (
    <ul
      ref={ref}
      dir="ltr"
      className={cn("align-with-page font-mono break-all", className, overflows && OVERFLOW_CLIP)}
    >
      {paths.map((path) => (
        <li key={path}>{path}</li>
      ))}
    </ul>
  );
}
