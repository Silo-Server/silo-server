import { Fragment } from "react";

// The path separator, not interface text.
const SEPARATOR = "/";

/**
 * A URL, path, or file name in its own reading order on a page of either direction. The
 * value runs left to right with each "/"-separated segment isolated, so an Arabic or Hebrew
 * segment reads in its own direction without trading places with its neighbours. Elements,
 * not isolate characters, keep copied text clean.
 */
export function PathText({ value }: { value: string }) {
  return (
    <bdi dir="ltr">
      {value.split(SEPARATOR).map((segment, index) => (
        // Segments never reorder, so the index is a stable key.
        <Fragment key={index}>
          {index > 0 && SEPARATOR}
          <bdi dir="auto">{segment}</bdi>
        </Fragment>
      ))}
    </bdi>
  );
}
