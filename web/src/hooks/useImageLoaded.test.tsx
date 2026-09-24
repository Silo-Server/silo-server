import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { imageIdentity, useImageLoaded } from "./useImageLoaded";

const REV_A = "3f9a".repeat(16);
const REV_B = "8c21".repeat(16);
const ART = "tmdb/movies/550/poster";

function localURL(key: string, exp: number, sig: string): string {
  return `/api/v2/artwork/${key}?exp=${exp}&sig=${sig}`;
}

/**
 * Renders the hook at `first`, fires its load event, then switches to `next`.
 * Returns how many times the loaded state fell back to false after the load,
 * which is how many times a consumer hides the image and shows its
 * placeholder again.
 */
function resetsAfterSwitch(first: string, next: string): number {
  const history: boolean[] = [];
  const { result, rerender } = renderHook(
    ({ url }: { url: string }) => {
      const state = useImageLoaded(url);
      history.push(state.loaded);
      return state;
    },
    { initialProps: { url: first } },
  );
  act(() => result.current.onLoad());
  expect(result.current.loaded).toBe(true);
  const loadedAt = history.length;
  rerender({ url: next });
  return history.slice(loadedAt).filter((loaded) => !loaded).length;
}

describe("imageIdentity", () => {
  it("drops the query of a revisioned artwork URL", () => {
    const key = `${ART}/w500.${REV_A}.webp`;
    expect(imageIdentity(localURL(key, 1758621600, "aaaa"))).toBe(`/api/v2/artwork/${key}`);
    expect(
      imageIdentity(
        `https://s3.example.com/silo/${ART}/original.${REV_A}.jpg?X-Amz-Signature=a#top`,
      ),
    ).toBe(`https://s3.example.com/silo/${ART}/original.${REV_A}.jpg`);
  });

  it("keeps the whole URL of anything else", () => {
    for (const url of [
      localURL(`${ART}/original.webp`, 1, "a"),
      localURL("library-posters/7.jpg", 1, "a"),
      localURL(`${ART}/poster.${REV_A}.webp`, 1, "a"),
      localURL(`${ART}/w500.backup.webp`, 1, "a"),
      "https://images.example.com/poster.jpg?w=300",
      "https://api.dicebear.com/9.x/shapes/svg?seed=abc",
      `/api/v2/artwork/${ART}/w500.${REV_A}.webp`,
      "",
    ]) {
      expect(imageIdentity(url)).toBe(url);
    }
  });
});

describe("useImageLoaded", () => {
  it("starts unloaded and loads on the image's load event", () => {
    const { result } = renderHook(() =>
      useImageLoaded(localURL(`${ART}/w500.${REV_A}.webp`, 1, "a")),
    );
    expect(result.current.loaded).toBe(false);
    act(() => result.current.onLoad());
    expect(result.current.loaded).toBe(true);
  });

  it("reports no image as unloaded", () => {
    const { result } = renderHook(() => useImageLoaded(undefined));
    act(() => result.current.onLoad());
    expect(result.current.loaded).toBe(false);
  });

  it("stays loaded when only a local artwork signature changes", () => {
    const key = `${ART}/w500.${REV_A}.webp`;
    expect(
      resetsAfterSwitch(localURL(key, 1758621600, "aaaa"), localURL(key, 1758622500, "bbbb")),
    ).toBe(0);
  });

  it("stays loaded when an S3 presigned revision is signed again", () => {
    const path = `https://bucket.s3.example.com/silo/${ART}/original.${REV_A}.jpg`;
    const presigned = (date: string, sig: string) =>
      `${path}?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=KEY%2F20260923%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=${date}&X-Amz-Expires=14400&X-Amz-SignedHeaders=host&x-id=GetObject&X-Amz-Signature=${sig}`;
    expect(
      resetsAfterSwitch(
        presigned("20260923T101500Z", "aa11"),
        presigned("20260923T101501Z", "bb22"),
      ),
    ).toBe(0);
  });

  it("stays loaded when a token-authenticated CDN revision is signed again", () => {
    const path = `https://cdn.example.com/${ART}/w300.${REV_A}.webp`;
    expect(
      resetsAfterSwitch(`${path}?verify=1758621300-abc%3D`, `${path}?verify=1758621301-def%3D`),
    ).toBe(0);
  });

  it("resets when the artwork revision changes", () => {
    expect(
      resetsAfterSwitch(
        localURL(`${ART}/w500.${REV_A}.webp`, 1758621600, "aaaa"),
        localURL(`${ART}/w500.${REV_B}.webp`, 1758621600, "bbbb"),
      ),
    ).toBe(1);
  });

  it("resets when the variant changes", () => {
    expect(
      resetsAfterSwitch(
        localURL(`${ART}/w300.${REV_A}.webp`, 1758621600, "aaaa"),
        localURL(`${ART}/w500.${REV_A}.webp`, 1758621600, "bbbb"),
      ),
    ).toBe(1);
  });

  it("resets when a mutable artwork URL is signed again", () => {
    // Legacy and uploaded keys are replaced in place, so a new signature may
    // carry new bytes.
    for (const key of [
      `${ART}/original.webp`,
      "library-posters/7.jpg",
      "profile-avatars/3/p1/w256.webp",
    ]) {
      expect(
        resetsAfterSwitch(localURL(key, 1758621600, "aaaa"), localURL(key, 1758622500, "bbbb")),
      ).toBe(1);
    }
  });

  it("hides the image again when a re-signed request fails", () => {
    const key = `${ART}/w500.${REV_A}.webp`;
    const { result, rerender } = renderHook(({ url }: { url: string }) => useImageLoaded(url), {
      initialProps: { url: localURL(key, 1758621600, "aaaa") },
    });
    act(() => result.current.onLoad());
    rerender({ url: localURL(key, 1758622500, "bbbb") });
    expect(result.current.loaded).toBe(true);

    // The element drops its kept pixels on error, so the placeholder must show.
    act(() => result.current.onError());
    expect(result.current.loaded).toBe(false);

    // A later signature that loads brings the image back.
    rerender({ url: localURL(key, 1758623400, "cccc") });
    act(() => result.current.onLoad());
    expect(result.current.loaded).toBe(true);
  });

  it("keeps a failed first load hidden", () => {
    const { result } = renderHook(() =>
      useImageLoaded(localURL(`${ART}/w500.${REV_A}.webp`, 1, "a")),
    );
    act(() => result.current.onError());
    expect(result.current.loaded).toBe(false);
  });

  it("resets when the query of an unrecognized URL changes", () => {
    expect(
      resetsAfterSwitch(
        "https://images.example.com/poster.jpg?w=300",
        "https://images.example.com/poster.jpg?w=500",
      ),
    ).toBe(1);
  });
});
