import { Fragment, type ReactNode } from "react";

// Only the first folders show before a path is cut, so only the folders before
// the first this-many separators are isolated, and the rest stays one text
// node. An audit row can hold any request path, and one with thousands of
// separators must not become thousands of elements.
const MAX_SEPARATORS = 32;

// From a URL's query (`?`) or fragment (`#`) on, these also separate parts, so an
// Arabic query value can't trade places with the `&name=value` after it.
const QUERY_SEPARATORS = "/?&=#";

/** Where a `/` path's query or a URL's fragment starts, or -1. */
function queryStart(path: string) {
  const query = path.indexOf("?");
  // `#` is a folder-name character in a file path, so only a URL has a fragment.
  const fragment = path.includes("://") ? path.indexOf("#") : -1;
  if (query < 0 || fragment < 0) return Math.max(query, fragment);
  return Math.min(query, fragment);
}

/**
 * A file path, or a URL, that reads left to right, folder by folder, in any
 * language, while each folder name keeps its own direction: one named in Arabic
 * reads right to left without reversing the folders around it. Put it in a
 * `dir="ltr"` element; cut short with an ellipsis, the path then keeps its
 * start. Splits on `/`, or on `\` in a Windows path with no `/`, and a URL's
 * query and fragment also on `? & = #`. Keeps every character as written.
 */
export function PathText({ path }: { path: string }) {
  // A backslash is part of a folder name in a POSIX path or a URL.
  const separator = path.includes("/") ? "/" : "\\";
  const query = separator === "/" ? queryStart(path) : -1;
  const nextSeparator = (from: number) => {
    for (let index = from; index < path.length; index++) {
      const char = path.charAt(index);
      if (char === separator || (query >= 0 && index >= query && QUERY_SEPARATORS.includes(char)))
        return index;
    }
    return -1;
  };
  const nodes: ReactNode[] = [];
  let start = 0;
  for (let separators = 0; separators < MAX_SEPARATORS; separators++) {
    const end = nextSeparator(start);
    if (end < 0) break;
    if (end > start) nodes.push(<bdi key={start}>{path.slice(start, end)}</bdi>);
    nodes.push(path.charAt(end));
    start = end + 1;
  }
  const rest = path.slice(start);
  if (rest) {
    // The last name, or everything past the limit as plain text.
    nodes.push(nextSeparator(start) >= 0 ? rest : <bdi key={start}>{rest}</bdi>);
  }
  return nodes;
}

/**
 * The element for a message's rich-text `code` tag that holds a path
 * (`Moves to <code>{dir}</code>`), as
 * `code: (chunks) => <PathCode>{chunks}</PathCode>`: left to right, with each
 * folder name in its own direction through `PathText`.
 */
export function PathCode({ children, className }: { children: ReactNode[]; className?: string }) {
  return (
    <code dir="ltr" className={className}>
      {children.map((chunk, index) => (
        <Fragment key={index}>
          {typeof chunk === "string" ? <PathText path={chunk} /> : chunk}
        </Fragment>
      ))}
    </code>
  );
}
