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
});
