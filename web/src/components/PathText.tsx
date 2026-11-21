import { Fragment } from "react";

// Path and URL delimiters, not interface text. The capture group keeps them in the split.
const DELIMITERS = /([/?&=#])/;

/**
 * A URL, path, or file name in its own reading order on a page of either direction. The
 * value runs left to right with each part between delimiters (`/ ? & = #`) isolated, so an
 * Arabic or Hebrew folder name or query value reads in its own direction without trading
 * places with its neighbours. Elements, not isolate characters, keep copied text clean.
 */
export function PathText({ value }: { value: string }) {
  return (
    <bdi dir="ltr">
      {value.split(DELIMITERS).map((part, index) => (
        // Parts never reorder, so the index is a stable key. Odd indexes are delimiters.
        <Fragment key={index}>{index % 2 ? part : <bdi dir="auto">{part}</bdi>}</Fragment>
      ))}
    </bdi>
  );
}
