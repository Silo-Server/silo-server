import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { WatchedCheckIndicator } from "./CardWatchedBadge";

describe("WatchedCheckIndicator", () => {
  it("keeps the accessible episode-row circle check", () => {
    const markup = renderToStaticMarkup(<WatchedCheckIndicator className="ml-auto" />);

    expect(markup).toContain('data-watched-indicator="icon-only"');
    expect(markup).toContain('role="img"');
    expect(markup).toContain('aria-label="Watched"');
    expect(markup).toContain("lucide-circle-check");
    expect(markup).toContain("ml-auto");
    expect(markup).not.toContain(">Watched<");
  });
});
