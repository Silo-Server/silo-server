import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { QueryDefinition } from "@/api/types";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";

import { RuleBuilder } from "./RuleBuilder";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
installV2Recorder();

beforeAll(() => {
  // Radix Select reads pointer capture and scrolls options, which jsdom lacks.
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

function renderBuilder(
  value: QueryDefinition,
  onChange: (value: QueryDefinition) => void = () => {},
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RuleBuilder
        value={value}
        libraries={[
          { id: 1, name: "Movies" },
          { id: 2, name: "4K Movies" },
        ]}
        onChange={onChange}
      />
    </QueryClientProvider>,
  );
}

function languageRule(field: string, value: string): QueryDefinition {
  return {
    library_ids: [1],
    media_scope: "movie",
    match: "all",
    groups: [{ match: "all", rules: [{ field, op: "is", value }] }],
    sort: { field: "title", order: "asc" },
  };
}

describe("RuleBuilder value pickers", () => {
  it("look for values in the rule's libraries and kind of titles", async () => {
    v2Recorder.answer("GET /api/v2/catalog/search/capabilities", {
      revision: "r1",
      state: "available",
      allowed: true,
      facet_value_search: true,
    });
    v2Recorder.answer("GET /api/v2/catalog/filters/search", {
      matches: ["Drama"],
      values: [{ value: "Drama", count: 12 }],
      has_more: false,
    });
    renderBuilder({
      library_ids: [2, 1],
      media_scope: "series",
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "" }] }],
      sort: { field: "title", order: "asc" },
    });

    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    expect(await screen.findByRole("option", { name: "Drama, 12 titles" })).toBeInTheDocument();
    await waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/catalog/filters/search").map((c) => c.query)).toEqual([
        {
          source: "query",
          facet: "genre",
          q: "",
          limit: 50,
          type: "series",
          library_ids: ["1", "2"],
        },
      ]),
    );
  });

  it("pick an audio language by name from the languages titles have", async () => {
    v2Recorder.answer("GET /api/v2/catalog/filters", {
      genres: [],
      studios: [],
      networks: [],
      countries: [],
      original_languages: ["fr"],
      content_ratings: [],
      technical: { resolutions: [], audio_languages: ["ja", "en"], subtitle_languages: [] },
    });
    const onChange = vi.fn();
    renderBuilder(languageRule("audio_language", ""), onChange);

    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    const options = await screen.findAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual(["English", "Japanese"]);
    await userEvent.click(screen.getByRole("option", { name: "Japanese" }));
    // Only the languages of the rule's libraries and kind of titles.
    expect(v2Recorder.callsOf("GET /api/v2/catalog/filters").map((c) => c.query)).toEqual([
      { source: "query", type: "movie", library_ids: ["1"] },
    ]);
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({
        groups: [{ match: "all", rules: [{ field: "audio_language", op: "is", value: "ja" }] }],
      }),
    );
  });

  it("say the languages failed to load and ask again when the picker opens", async () => {
    let failed = false;
    v2Recorder.answer("GET /api/v2/catalog/filters", () => {
      if (!failed) {
        failed = true;
        throw new Error("network down");
      }
      return {
        genres: [],
        studios: [],
        networks: [],
        countries: [],
        original_languages: ["fr"],
        content_ratings: [],
      };
    });
    renderBuilder(languageRule("original_language", ""));

    const value = screen.getByRole("combobox", { name: "Value" });
    await waitFor(() => expect(value).toHaveTextContent("Couldn’t load languages"));
    await userEvent.click(value);
    expect(await screen.findByRole("option", { name: "French" })).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(v2Recorder.callsOf("GET /api/v2/catalog/filters")).toHaveLength(2);
  });

  it("ask an older server for the languages of the whole kind, since it refuses library_ids", async () => {
    v2Recorder.answer("GET /api/v2/catalog/search/capabilities", {
      revision: "r1",
      state: "available",
      allowed: true,
    });
    v2Recorder.answer("GET /api/v2/catalog/filters", {
      genres: [],
      studios: [],
      networks: [],
      countries: [],
      original_languages: ["fr"],
      content_ratings: [],
    });
    renderBuilder(languageRule("original_language", ""));

    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    expect(await screen.findByRole("option", { name: "French" })).toBeInTheDocument();
    expect(v2Recorder.callsOf("GET /api/v2/catalog/filters").map((c) => c.query)).toEqual([
      { source: "query", type: "movie", skip_technical: true },
    ]);
  });

  it("ask again with the rule's libraries when the capability check failed", async () => {
    let failed = false;
    v2Recorder.answer("GET /api/v2/catalog/search/capabilities", () => {
      if (!failed) {
        failed = true;
        throw new Error("network down");
      }
      return { revision: "r1", state: "available", allowed: true, facet_value_search: true };
    });
    v2Recorder.answer("GET /api/v2/catalog/filters", {
      genres: [],
      studios: [],
      networks: [],
      countries: [],
      original_languages: ["fr"],
      content_ratings: [],
    });
    renderBuilder(languageRule("original_language", ""));

    const value = screen.getByRole("combobox", { name: "Value" });
    await waitFor(() => expect(value).toHaveTextContent("Couldn’t load languages"));
    await userEvent.click(value);
    expect(await screen.findByRole("option", { name: "French" })).toBeInTheDocument();
    expect(v2Recorder.callsOf("GET /api/v2/catalog/filters").map((c) => c.query)).toEqual([
      { source: "query", type: "movie", library_ids: ["1"], skip_technical: true },
    ]);
  });

  it("keep a saved language no title has any more", async () => {
    v2Recorder.answer("GET /api/v2/catalog/filters", {
      genres: [],
      studios: [],
      networks: [],
      countries: [],
      original_languages: ["fr"],
      content_ratings: [],
    });
    renderBuilder(languageRule("original_language", "de"));

    const value = screen.getByRole("combobox", { name: "Value" });
    await waitFor(() => expect(value).toHaveTextContent("German"));
    await userEvent.click(value);
    const options = await screen.findAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual(["French", "German"]);
  });
});
