import { describe, expect, it } from "vitest";
import { chromaSitingOf, pictureColorOf } from "./pictureColor";

describe("chromaSitingOf", () => {
  it("reads AV1's signalled chroma sample position", () => {
    // av01.P.LLT.DD.M.XYP: the third digit of the subsampling field.
    expect(chromaSitingOf("av01.0.08M.10.0.111.09.16.09.0")).toBe("left");
    expect(chromaSitingOf("av01.0.08M.10.0.112.09.16.09.0")).toBe("top-left");
  });

  it("keeps the centre for an AV1 stream that leaves the position unknown", () => {
    expect(chromaSitingOf("av01.0.08M.10.0.110.09.16.09.0")).toBe("center");
    // A short codec string carries no subsampling field at all.
    expect(chromaSitingOf("av01.0.08M.10")).toBe("center");
  });

  it("uses type 0 for H.264 and HEVC, the position of an unsignalled stream", () => {
    expect(chromaSitingOf("hvc1.2.4.L153.B0")).toBe("left");
    expect(chromaSitingOf("hev1.2.4.L153.B0")).toBe("left");
    expect(chromaSitingOf("avc1.640028")).toBe("left");
  });

  it("keeps the centre for VP9, which signals no siting", () => {
    // Mediabunny fills the subsampling field with a default, so it says nothing.
    expect(chromaSitingOf("vp09.02.40.10.01.09.16.09.00")).toBe("center");
  });
});

describe("pictureColorOf", () => {
  it("takes the stream's primaries and matrix", () => {
    expect(
      pictureColorOf({
        codec: "vp09.02.40.10",
        colorSpace: { primaries: "bt709", transfer: "hlg" as never, matrix: "bt709" },
      }),
    ).toEqual({ primaries: "bt709", matrix: "bt709", chromaSiting: "center" });
    expect(
      pictureColorOf({
        codec: "hvc1.2.4.L153.B0",
        colorSpace: { primaries: "smpte432" as never, matrix: "bt2020-ncl" as never },
      }),
    ).toEqual({ primaries: "p3", matrix: "bt2020", chromaSiting: "left" });
  });

  it("assumes BT.2020 when the stream does not say", () => {
    expect(pictureColorOf({ codec: "av01.0.08M.10" })).toEqual({
      primaries: "bt2020",
      matrix: "bt2020",
      chromaSiting: "center",
    });
  });
});
