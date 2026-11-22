import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { NodeName } from "./NodeName";

/** The name box's `dir` and the text of each piece it shows. */
function pieces(name: string) {
  const html = renderToStaticMarkup(<NodeName name={name} />);
  const dir = /^<span[^>]* dir="([^"]+)"/.exec(html)?.[1];
  const texts = [...html.matchAll(/<span class="[^"]*">([^<]*)<\/span>/g)].map((m) => m[1]);
  return { dir, texts };
}

describe("NodeName", () => {
  it("keeps the end of a Latin name and cuts an Arabic one between words", () => {
    expect(pieces("gpu-transcode-frankfurt-east-rack-07")).toEqual({
      dir: "ltr",
      texts: ["gpu-transcode-frankfurt-", "east-rack-07"],
    });
    expect(pieces("خادم البث الرئيسي في فرانكفورت")).toEqual({
      dir: "rtl",
      texts: ["خادم البث الرئيسي ", "في فرانكفورت"],
    });
  });

  it("doesn't cut a name where its pieces would read in a different order", () => {
    // Brackets pair across the whole name (a stray closing one or a quotation
    // mark changes nothing), a mark or an override sets the direction of the
    // text after it, and a Syriac first letter makes the name right to left.
    for (const name of [
      "ܬܪܢܣܟܘܕ-worker-east-01",
      "gpu אבג(דהו abc12345678)",
      "gpu אבג(דהו] abc12345678)",
      "gpu \u301dאבג(דהו\u301e abc12345678)",
      "gpu-\u202eabc\u202c transcode-worker-07",
      "\u200fgpu-transcode-frankfurt-east-rack-07",
    ]) {
      expect(pieces(name)).toEqual({ dir: "auto", texts: [name] });
    }
  });

  it("cuts after a closed bracket or a non-joiner, not between joined letters", () => {
    expect(pieces("gpu （אבג） transcode-rack-07")).toEqual({
      dir: "ltr",
      texts: ["gpu （אבג） trans", "code-rack-07"],
    });
    expect(pieces("سرور\u200cهای\u200cپخش\u200cفرانکفورت")).toEqual({
      dir: "rtl",
      texts: ["سرور\u200cهای\u200c", "پخش\u200cفرانکفورت"],
    });
    // A mark and a right-to-left mark don't stop two Arabic letters joining.
    expect(pieces("خادم البث ب\u0300\u200fفرانكفورت ال")).toEqual({
      dir: "rtl",
      texts: ["خادم البث ", "ب\u0300\u200fفرانكفورت ال"],
    });
    // Nor does a zero-width space, and a joiner before a mark joins what follows.
    for (const between of ["\u200b", "\u200d\u200f"]) {
      expect(pieces(`خادم البث ب${between}فرانكفورت ال`).texts).toEqual([
        "خادم البث ",
        `ب${between}فرانكفورت ال`,
      ]);
    }
  });

  it("doesn't start the end piece with a zero-width joiner", () => {
    const name = "render-node-\u{1f468}\u200d\u{1f469}\u200d\u{1f467}-family-box";
    expect(pieces(name)).toEqual({
      dir: "ltr",
      texts: ["render-node-\u{1f468}\u200d\u{1f469}\u200d\u{1f467}", "-family-box"],
    });
  });
});
