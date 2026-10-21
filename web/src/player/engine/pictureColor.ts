/**
 * How the engine's HDR presenter interprets decoded samples, resolved in the
 * worker from the stream's own decoder configuration before the worker
 * re-tags it (see engine.worker.ts).
 */

/** Primaries the source's R'G'B' is expressed in. */
export type SourcePrimaries = "bt2020" | "bt709" | "p3";

/** Y'CbCr to R'G'B' matrix of the source's samples. */
export type SourceMatrix = "bt2020" | "bt709";

/**
 * Where the first chroma sample sits relative to the luma grid:
 * `center` between four luma samples, `left` horizontally co-sited and
 * vertically between two rows (H.264/HEVC chroma_sample_loc_type 0), and
 * `top-left` co-sited with the first luma sample (type 2).
 */
export type ChromaSiting = "center" | "left" | "top-left";

export interface PictureColor {
  primaries: SourcePrimaries;
  matrix: SourceMatrix;
  chromaSiting: ChromaSiting;
}

/**
 * The tag the worker gives HDR frames so that importing one into WebGPU
 * applies only the Y'CbCr matrix and leaves the PQ/HLG codes for the shader.
 */
export const IMPORT_COLOR_TAG = { primaries: "bt709", transfer: "iec61966-2-1" } as const;

/** What the presenter assumed before the stream's own description was read. */
export const DEFAULT_PICTURE_COLOR: PictureColor = {
  primaries: "bt2020",
  matrix: "bt2020",
  chromaSiting: "center",
};

function primariesOf(value: string | null | undefined): SourcePrimaries {
  switch (value) {
    case "bt709":
      return "bt709";
    case "smpte432":
      return "p3";
    default:
      // Anything else, said or not, is read as BT.2020, the gamut of HDR.
      return "bt2020";
  }
}

function matrixOf(value: string | null | undefined): SourceMatrix {
  return value === "bt709" ? "bt709" : "bt2020";
}

/**
 * Chroma siting the codec itself signals or defines. Only AV1 carries it in
 * the configuration the engine sees: `chroma_sample_position` is the last
 * digit of the codec string's subsampling field (`av01.P.LLT.DD.M.XYP`).
 * H.264 and HEVC define type 0 as the position of an unsignalled stream; an
 * explicit VUI value is not read. VP9 has no siting at all, and an AV1
 * stream that leaves it unknown says nothing either, so both stay centred.
 */
export function chromaSitingOf(codec: string): ChromaSiting {
  const parts = codec.split(".");
  switch (parts[0]) {
    case "av01": {
      const position = parts[5]?.[2];
      if (position === "1") return "left";
      if (position === "2") return "top-left";
      return "center";
    }
    case "avc1":
    case "avc3":
    case "hvc1":
    case "hev1":
      return "left";
    default:
      return "center";
  }
}

/** The presenter's view of a stream, from its decoder configuration. */
export function pictureColorOf(
  config: Pick<VideoDecoderConfig, "codec" | "colorSpace">,
): PictureColor {
  // TypeScript's DOM types predate several of these colour values.
  const colorSpace = config.colorSpace as
    | { primaries?: string | null; matrix?: string | null }
    | undefined;
  return {
    primaries: primariesOf(colorSpace?.primaries),
    matrix: matrixOf(colorSpace?.matrix),
    chromaSiting: chromaSitingOf(config.codec),
  };
}
