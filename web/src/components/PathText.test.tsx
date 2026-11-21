import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { PathText } from "./PathText";

describe("PathText", () => {
  it("isolates only the first folders of a path with many separators", () => {
    // An audit row can hold any request path, so a hostile one must not
    // become one element per folder.
    const path = "/a".repeat(100_000);
    const html = renderToStaticMarkup(
      <span>
        <PathText path={path} />
      </span>,
    );
    expect(html.match(/<bdi>/g)?.length).toBeLessThanOrEqual(32);
    expect(html.replace(/<[^>]+>/g, "")).toBe(path);
  });
});
