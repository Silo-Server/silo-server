import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ChartEmptyState, ChartSkeleton, LineChart, StackedColumnChart } from ".";

/**
 * A `fill` chart takes its height from the widget's grid rows, which are only
 * fixed from lg up. Below that the rows are content-sized, so a plot with no
 * floor of its own measures 0px and draws nothing on phones and tablets. The
 * layout itself is CSS jsdom cannot compute, so this checks the contract: the
 * fill charts carry `.admin-chart-fill`, and app.css drops its floor only in
 * the block that fixes the row height.
 */
const css = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../../../app.css"),
  "utf8",
);

/** The `@media` block that encloses the first occurrence of `anchor`. */
function enclosingMediaBlock(anchor: string): string {
  const at = css.indexOf(anchor);
  expect(at, `missing: ${anchor}`).toBeGreaterThan(-1);
  const start = css.lastIndexOf("@media", at);
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) return css.slice(start, i + 1);
  }
  throw new Error(`unterminated media block around: ${anchor}`);
}

function ruleBody(source: string, selector: string): string {
  const start = source.indexOf(`${selector} {`);
  expect(start, `missing rule: ${selector}`).toBeGreaterThan(-1);
  return source.slice(source.indexOf("{", start), source.indexOf("}", start));
}

describe("fill chart height", () => {
  it("keeps a plot floor wherever the widget rows are content-sized", () => {
    const base = ruleBody(css, ".admin-chart-fill");
    expect(base).toMatch(/min-height:\s*10rem/);
    expect(base).toMatch(/flex:\s*1 1 0%/);

    const fixedRows = enclosingMediaBlock("grid-auto-rows: var(--admin-row-h)");
    expect(fixedRows).toMatch(/^@media \(min-width: 1024px\)/);
    expect(ruleBody(fixedRows, ".admin-chart-fill")).toMatch(/min-height:\s*0/);
  });

  it("puts the fill class on every fill-mode plot", () => {
    const { container } = render(
      <>
        <StackedColumnChart
          fill
          buckets={[{ t: 0, segments: [1, 0, 2] }]}
          seriesLabels={["Direct play", "Direct stream", "Transcode"]}
          ariaLabel="columns"
        />
        <LineChart
          fill
          points={[
            { t: 0, value: 1 },
            { t: 60_000, value: 3 },
          ]}
          seriesLabel="Egress"
          ariaLabel="line"
        />
        <ChartEmptyState fill message="Nothing yet" />
        <ChartSkeleton fill />
      </>,
    );

    expect(screen.getByRole("img", { name: "columns" }).parentElement).toHaveClass(
      "admin-chart-fill",
    );
    // The SVG sits in the plot box; the fill class belongs on the row that
    // holds the plot beside its tick labels.
    expect(screen.getByRole("img", { name: "line" }).parentElement?.parentElement).toHaveClass(
      "admin-chart-fill",
    );
    expect(screen.getByText("Nothing yet").parentElement).toHaveClass("admin-chart-fill");
    expect(container.querySelector('[data-slot="skeleton"]')).toHaveClass("admin-chart-fill");
  });
});
