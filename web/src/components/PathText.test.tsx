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

  it("isolates a URL's query values but not a '#' in a file name", () => {
    const bdis = (path: string) =>
      [...renderToStaticMarkup(<PathText path={path} />).matchAll(/<bdi>(.*?)<\/bdi>/g)].map(
        (match) => match[1],
      );
    // An Arabic query value must not run together with the `&limit=10` after it.
    expect(bdis("https://example.com/seed.json.gz?prefix=/تجربة&limit=10#نسخة")).toEqual([
      "https:",
      "example.com",
      "seed.json.gz",
      "prefix",
      "تجربة",
      "limit",
      "10",
      "نسخة",
    ]);
    expect(bdis("/media/كتب #1/a=b.mkv")).toEqual(["media", "كتب #1", "a=b.mkv"]);
  });
});
