import { describe, expect, it } from "vitest";
import { redactStreamUrls } from "./source";

describe("redactStreamUrls", () => {
  it("removes stream URLs and keeps the rest of a fetch error", () => {
    expect(
      redactStreamUrls(
        "Could not read the file: Error: Error fetching https://media.example/stream/direct/eyJh.eyJz.c2ln: 503 Service Unavailable",
      ),
    ).toBe("Could not read the file: Error: Error fetching <stream URL>: 503 Service Unavailable");
    expect(redactStreamUrls("Error fetching http://host:8090/s?token=abc")).toBe(
      "Error fetching <stream URL>",
    );
    expect(redactStreamUrls("Decoder closed")).toBe("Decoder closed");
  });

  it("removes same-origin stream paths and their tokens", () => {
    const url = "/api/v2/stream/direct/eyJh.eyJz.c2ln?st=abc&token=def";
    expect(redactStreamUrls(`Error fetching ${url}: 401 Unauthorized`, url)).toBe(
      "Error fetching <stream URL>: 401 Unauthorized",
    );
    // Without the plan's URL to match, a relative path with a token still goes.
    expect(redactStreamUrls(`Error fetching ${url}: 401 Unauthorized`)).toBe(
      "Error fetching <stream URL>: 401 Unauthorized",
    );
  });
});
