import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { PathText } from "./PathText";

describe("PathText", () => {
  it("isolates only the folders before the first 32 separators", () => {
    // An audit row can hold any request path, so a hostile one must not
    // become one element per folder.
    const path = "/a".repeat(33);
    const html = renderToStaticMarkup(
      <span>
        <PathText path={path} />
      </span>,
    );
    expect(html.match(/<bdi>/g)?.length).toBeLessThanOrEqual(32);
    expect(html.replace(/<[^>]+>/g, "")).toBe(path);
  });
});
