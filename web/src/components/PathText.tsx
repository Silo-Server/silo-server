/**
 * A file path, or a URL's path, that reads left to right, folder by folder, in
 * any language, while each folder name keeps its own direction: one named in
 * Arabic reads right to left without reversing the folders around it. Put it
 * in a `dir="ltr"` element; cut short with an ellipsis, the path then keeps
 * its start. Splits on `/`, or on `\` in a Windows path with no `/`, and keeps
 * each separator as written.
 */
export function PathText({ path }: { path: string }) {
  // A backslash is part of a folder name in a POSIX path or a URL. The capture
  // group keeps the separators: names at even indexes, separators at odd.
  return path
    .split(path.includes("/") ? /(\/)/ : /(\\)/)
    .map((part, index) => (index % 2 === 1 ? part : part ? <bdi key={index}>{part}</bdi> : null));
}
