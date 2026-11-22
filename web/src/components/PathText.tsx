import { Fragment, type ReactNode } from "react";

// Only the first folders show before a path is cut, so only the folders before
// the first this-many separators are isolated, and the rest stays one text
// node. An audit row can hold any request path, and one with thousands of
// separators must not become thousands of elements.
const MAX_SEPARATORS = 32;

// In an http(s) URL, the query and fragment also split on these, so an Arabic query
// value can't trade places with the `&name=value` after it. A file path keeps them
// as part of its folder names ("What If...?", "Tom & Jerry").
const QUERY_SEPARATORS = "?&=#";
const HTTP_URL = /^https?:\/\//i;

/**
 * A file path, or a URL, that reads left to right, folder by folder, in any
 * language, while each folder name keeps its own direction: one named in Arabic
 * reads right to left without reversing the folders around it. Put it in a
 * `dir="ltr"` element; cut short with an ellipsis, the path then keeps its
 * start. Splits on `/`, or on `\` in a Windows path with no `/`, and an http(s)
 * URL's query and fragment also on `? & = #`. Keeps every character as written.
 */
export function PathText({ path }: { path: string }) {
  // A backslash is part of a folder name in a POSIX path or a URL.
  const separator = path.includes("/") ? "/" : "\\";
  const query = HTTP_URL.test(path) ? path.search(/[?#]/) : -1;
  const nextSeparator = (from: number) => {
    const end = path.indexOf(separator, from);
    if (query < 0) return end;
    const stop = end < 0 ? path.length : end;
    for (let index = Math.max(from, query); index < stop; index++) {
      if (QUERY_SEPARATORS.includes(path.charAt(index))) return index;
    }
    return end;
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
