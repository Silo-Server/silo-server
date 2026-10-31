// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

// @ts-expect-error The vendored JavaScript module does not ship TypeScript declarations.
import { search } from "../../vendor/foliate-js/search.js";
// @ts-expect-error The vendored JavaScript module does not ship TypeScript declarations.
import { SEARCH_RESULT_LIMIT } from "../../vendor/foliate-js/view.js";

type Excerpt = { pre: string; match: string; post: string };
type Match = {
  range: { startIndex: number; startOffset: number; endIndex: number; endOffset: number };
  excerpt: Excerpt;
};

const CONTEXT_LENGTH = 50;

// "base" runs the segmenter search and "variant" the plain substring search;
// both build excerpts the same way.
function findAll(strs: string[], query: string, sensitivity = "base"): Match[] {
  return Array.from(
    search(strs, query, { granularity: "grapheme", sensitivity }) as Iterable<Match>,
  );
}

function firstExcerpt(strs: string[], query: string): Excerpt | undefined {
  return findAll(strs, query)[0]?.excerpt;
}

// The excerpt foliate built before Silo bounded it: whitespace normalized
// across each whole text node.
function wholeNodeExcerpt(strs: string[], range: Match["range"]): Pick<Excerpt, "pre" | "post"> {
  const normalize = (str: string) => str.replace(/\s+/g, " ");
  const before = normalize((strs[range.startIndex] ?? "").slice(0, range.startOffset)).trimStart();
  const after = normalize((strs[range.endIndex] ?? "").slice(range.endOffset)).trimEnd();
  return {
    pre: `${before.length < CONTEXT_LENGTH ? "" : "…"}${before.slice(-CONTEXT_LENGTH)}`,
    post: `${after.slice(0, CONTEXT_LENGTH)}${after.length < CONTEXT_LENGTH ? "" : "…"}`,
  };
}

describe("foliate search excerpts", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps short excerpts and matches that span text nodes", () => {
    expect(firstExcerpt(["Call me  Ishmael.\nSome years ago"], "ishmael")).toEqual({
      pre: "Call me ",
      match: "Ishmael",
      post: ". Some years ago",
    });
    const long = "x".repeat(60);
    expect(firstExcerpt([`${long} needle ${long}`], "needle")).toEqual({
      pre: `…${"x".repeat(49)} `,
      match: "needle",
      post: ` ${"x".repeat(49)}…`,
    });
    // A match across three nodes keeps the middle node in its text.
    expect(firstExcerpt(["one tw", "o thr", "ee four"], "two three")).toEqual({
      pre: "one ",
      match: "two three",
      post: " four",
    });
  });

  it("matches the whole-node excerpt for long and whitespace-heavy nodes", () => {
    const gap = (n: number) => " \n\t".repeat(n);
    const nodes = [
      `${"word ".repeat(300)}target${" word".repeat(300)}`,
      `${gap(400)}ab${gap(400)}`,
      `lead${gap(70)}${"a b ".repeat(10)}target${" a b".repeat(10)}${gap(70)}tail`,
      `${"abc def ".repeat(40)}${gap(500)}target${gap(500)}${"abc def ".repeat(40)}`,
      `${"y".repeat(198)}target${"y".repeat(202)}`,
    ];
    for (const sensitivity of ["base", "variant"]) {
      for (const node of nodes) {
        const strs = [node];
        const query = node.includes("target") ? "target" : "ab";
        const matches = findAll(strs, query, sensitivity);
        expect(matches.length, node.slice(0, 20)).toBeGreaterThan(0);
        for (const { range, excerpt } of matches) {
          expect(excerpt).toMatchObject(wholeNodeExcerpt(strs, range));
        }
      }
    }
  });

  it("normalizes a bounded window per match rather than the whole node", () => {
    const normalized: number[] = [];
    const replace = String.prototype.replace;
    vi.spyOn(String.prototype, "replace").mockImplementation(function (
      this: string,
      ...args: Parameters<typeof replace>
    ) {
      if (args[0] instanceof RegExp && args[0].source === "\\s+") normalized.push(this.length);
      return replace.apply(this, args);
    });

    // 6,000 characters and 2,000 matches; whole-node excerpts read ~6,000
    // characters for every match.
    const matches = findAll(["ab ".repeat(2000)], "ab");
    expect(matches).toHaveLength(2000);
    expect(normalized.length).toBeGreaterThanOrEqual(matches.length);
    const perMatch = normalized.reduce((sum, length) => sum + length, 0) / matches.length;
    expect(perMatch).toBeLessThan(CONTEXT_LENGTH * 10);
  });
});

describe("foliate view search", () => {
  type SearchItem = { cfi: string; excerpt: Excerpt };
  type SearchResult = "done" | { progress?: number; index?: number; subitems?: SearchItem[] };
  type TestView = HTMLElement & {
    book: unknown;
    getCFI: (index: number, range: Range) => string;
    addAnnotation: (annotation: { value: string }) => void;
    search: (opts: {
      query: string;
      limit?: number;
      signal?: AbortSignal;
    }) => AsyncGenerator<SearchResult>;
  };

  function sectionDocument(text: string): Document {
    const doc = document.implementation.createHTMLDocument("section");
    const p = doc.createElement("p");
    p.textContent = text;
    doc.body.appendChild(p);
    return doc;
  }

  function makeView(sectionTexts: string[]) {
    const view = document.createElement("foliate-view") as TestView;
    view.book = {
      sections: sectionTexts.map((text) => ({ createDocument: async () => sectionDocument(text) })),
    };
    const addAnnotation = vi.fn();
    view.addAnnotation = addAnnotation;
    return { view, addAnnotation };
  }

  async function collect(iter: AsyncGenerator<SearchResult>) {
    const subitems: SearchItem[] = [];
    for await (const result of iter) {
      if (result !== "done" && result.subitems) subitems.push(...result.subitems);
    }
    return subitems;
  }

  it("stops a whole-book search at the result limit", async () => {
    const perSection = "ab ".repeat(SEARCH_RESULT_LIMIT * 0.75);
    // The reader passes its own limit; other callers get the default.
    for (const [limit, expected] of [
      [undefined, SEARCH_RESULT_LIMIT],
      [250, 250],
    ]) {
      const { view, addAnnotation } = makeView([perSection, perSection, perSection]);

      const subitems = await collect(view.search({ query: "ab", limit }));

      expect(subitems).toHaveLength(expected);
      expect(addAnnotation).toHaveBeenCalledTimes(expected);
      expect(subitems[0]?.excerpt).toEqual({
        pre: "",
        match: "ab",
        post: ` ${"ab ".repeat(16)}a…`,
      });
    }
  });

  it("ends an aborted search inside a long section without publishing it", async () => {
    const { view, addAnnotation } = makeView(["ab ".repeat(SEARCH_RESULT_LIMIT * 5)]);
    const controller = new AbortController();
    const getCFI = view.getCFI.bind(view);
    let matched = 0;
    // Abort from a later task, the way a newer query or closing the reader does.
    view.getCFI = (index, range) => {
      if (matched++ === 0) setTimeout(() => controller.abort());
      return getCFI(index, range);
    };

    const subitems = await collect(view.search({ query: "ab", signal: controller.signal }));

    expect(controller.signal.aborted).toBe(true);
    expect(matched).toBeGreaterThan(0);
    expect(matched).toBeLessThan(SEARCH_RESULT_LIMIT);
    expect(subitems).toEqual([]);
    expect(addAnnotation).not.toHaveBeenCalled();
  });
});
