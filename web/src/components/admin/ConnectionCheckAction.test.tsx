import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { ConnectionCheckAction } from "./ConnectionCheckAction";

describe("ConnectionCheckAction", () => {
  it("announces connection results", () => {
    const markup = renderToStaticMarkup(
      <ConnectionCheckAction
        onClick={vi.fn()}
        result={{ success: true, message: "Connection successful." }}
      />,
    );

    expect(markup).toContain('role="status"');
    expect(markup).toContain('aria-live="polite"');
  });
});
