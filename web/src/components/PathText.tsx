import type { ReactNode } from "react";

// Only the first folders show before a path is cut, so only the folders before
// the first this-many separators are isolated, and the rest stays one text
// node. An audit row can hold any request path, and one with thousands of
// separators must not become thousands of elements.
const MAX_SEPARATORS = 32;

/**
 * A file path, or a URL's path, that reads left to right, folder by folder, in
 * any language, while each folder name keeps its own direction: one named in
 * Arabic reads right to left without reversing the folders around it. Put it
 * in a `dir="ltr"` element; cut short with an ellipsis, the path then keeps
 * its start. Splits on `/`, or on `\` in a Windows path with no `/`, and keeps
 * every character as written.
 */
export function PathText({ path }: { path: string }) {
  // A backslash is part of a folder name in a POSIX path or a URL.
  const separator = path.includes("/") ? "/" : "\\";
  const nodes: ReactNode[] = [];
  let start = 0;
  for (let separators = 0; separators < MAX_SEPARATORS; separators++) {
    const end = path.indexOf(separator, start);
    if (end < 0) break;
    if (end > start) nodes.push(<bdi key={start}>{path.slice(start, end)}</bdi>);
    nodes.push(separator);
    start = end + 1;
  }
  const rest = path.slice(start);
  if (rest) {
    // The last name, or everything past the limit as plain text.
    nodes.push(rest.includes(separator) ? rest : <bdi key={start}>{rest}</bdi>);
  }
  return nodes;
}
