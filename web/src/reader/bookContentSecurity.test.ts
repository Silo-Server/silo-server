import * as webStreams from "node:stream/web";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BOOK_DOCUMENT_CSP, sanitizeBookDocument } from "@/reader/bookContentSecurity";
import { DocumentLoader } from "@/reader/readest/libs/document";

const CHAPTER = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>One</title><link rel="stylesheet" href="style.css"/><style>p { color: red; }</style>
<script>parent.stolen = localStorage.getItem("silo_auth")</script><script src="app.js"></script></head>
<body>
<p id="intro" style="font-weight: bold" onclick="steal()">Chapter text</p>
<img src="cover.png" alt="javascript: the good parts" onerror="steal()"/>
<a href="chapter2.xhtml#x" epub:type="noteref">note</a>
<a href=" java&#x09;script:steal()">bad link</a>
<iframe srcdoc="&lt;script&gt;steal()&lt;/script&gt;" src="untyped.html"></iframe>
<noscript><p title="&lt;/noscript&gt;&lt;img src=x onerror=steal()&gt;">fallback</p></noscript>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
<script>steal()</script>
<a xlink:href="javascript:steal()"><text>svg link</text></a>
<a><set attributeName="xlink:href" to="javascript:steal()"/><text>animated</text></a>
<circle r="4" onload="steal()"/>
</svg>
</body></html>`;

const OPF = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="id">fixture</dc:identifier><dc:title>Fixture</dc:title><dc:language>en</dc:language>
</metadata>
<manifest>
<item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
<item id="c2" href="chapter2.html" media-type="text/html; charset=utf-8"/>
<item id="css" href="style.css" media-type="text/css"/>
<item id="js" href="app.js" media-type="application/javascript"/>
<item id="png" href="cover.png" media-type="image/png"/>
<item id="untyped" href="untyped.html"/>
</manifest>
<spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`;

async function fixtureEpub(): Promise<File> {
  // zip.js needs the WHATWG streams the jsdom VM context does not expose.
  for (const name of ["ReadableStream", "WritableStream", "TransformStream"] as const) {
    vi.stubGlobal(name, globalThis[name] ?? webStreams[name]);
  }
  const { TextReader, Uint8ArrayWriter, ZipWriter } = await import("@zip.js/zip.js");
  const writer = new ZipWriter(new Uint8ArrayWriter());
  const files: Record<string, string> = {
    mimetype: "application/epub+zip",
    "META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
<rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
    "content.opf": OPF,
    "chapter1.xhtml": CHAPTER,
    // Mislabelled so the vendored loader skips its own (X)HTML handling.
    "chapter2.html": `<html><body><p>Two</p><img src=x onerror="steal()"><script>steal()</script></body></html>`,
    "style.css": "p { margin: 0; }",
    "app.js": "steal()",
    "cover.png": "png",
    "untyped.html": "<script>steal()</script>",
  };
  for (const [name, text] of Object.entries(files)) {
    await writer.add(name, new TextReader(text), { level: 0, useWebWorkers: false });
  }
  return new File([await writer.close()], "fixture.epub", { type: "application/epub+zip" });
}

function expectInert(markup: string) {
  expect(markup).not.toMatch(/<(?:\w+:)?script/i);
  expect(markup).not.toMatch(/\son\w+=/i);
  expect(markup).not.toMatch(/(?:href|src|action)="\s*java\s*script:/i);
  expect(markup).not.toContain("srcdoc");
  expect(markup).not.toContain("<set");
}

describe("book content security", () => {
  let blobs: Map<string, Blob>;

  beforeEach(() => {
    blobs = new Map();
    let next = 0;
    vi.spyOn(URL, "createObjectURL").mockImplementation((blob) => {
      const url = `blob:book/${next++}`;
      blobs.set(url, blob as Blob);
      return url;
    });
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("strips active content from EPUB chapters before they reach a frame", async () => {
    const { book } = await new DocumentLoader(await fixtureEpub()).open();
    type LoadableSection = { load(): Promise<string>; loadContent(): Promise<string> };
    const [first, second] = book.sections as unknown as LoadableSection[];

    // Reflowable chapters render through srcdoc, fixed-layout ones through the
    // blob URL; both must carry the sanitized markup.
    const url = await first!.load();
    const srcdoc = await first!.loadContent();
    const blob = blobs.get(url)!;
    expect(blob.type).toBe("application/xhtml+xml");
    expect(await blob.text()).toBe(srcdoc);
    expectInert(srcdoc);
    expect(srcdoc).toContain(`content="${BOOK_DOCUMENT_CSP}"`);
    expect(srcdoc).not.toContain("noscript");
    // Ordinary book content survives.
    expect(srcdoc).toContain("p { color: red; }");
    expect(srcdoc).toMatch(/<p id="intro" style="font-weight: bold">Chapter text<\/p>/);
    expect(srcdoc).toContain('alt="javascript: the good parts"');
    expect(srcdoc).toContain('href="chapter2.xhtml#x" epub:type="noteref"');
    expect(srcdoc).toMatch(/<link rel="stylesheet" href="blob:book\/\d+"/);
    expect(srcdoc).toMatch(/<img src="blob:book\/\d+"/);
    expect(srcdoc).toContain("<text>svg link</text>");

    // The untyped resource behind the iframe must not be sniffed as HTML.
    const untyped = srcdoc.match(/<iframe src="(blob:book\/\d+)"/)?.[1];
    expect(blobs.get(untyped!)?.type).toBe("application/octet-stream");

    // A document type the vendored loader does not recognise is still sanitized.
    const secondBlob = blobs.get(await second!.load())!;
    expect(secondBlob.type).toBe("text/html; charset=utf-8");
    const secondMarkup = await secondBlob.text();
    expectInert(secondMarkup);
    expect(secondMarkup).toContain("<p>Two</p>");
    expect(secondMarkup).toContain(`content="${BOOK_DOCUMENT_CSP}"`);
  });

  it.each([
    [
      "XSLT stylesheet",
      "application/xml",
      `<?xml-stylesheet type="text/xsl" href="x.xsl"?><?xml-stylesheet type="text/css" href="a.css"?><root/>`,
      "x.xsl",
      "a.css",
    ],
    [
      "SVG document",
      "image/svg+xml",
      `<svg xmlns="http://www.w3.org/2000/svg"><script>steal()</script><animate attributeName="href" values="#a;javascript:steal()"/><rect width="1"/></svg>`,
      "steal()",
      '<rect width="1"/>',
    ],
    [
      "HTML without a head",
      "text/html",
      `<p onmouseover="steal()">x</p><form action="javascript:steal()"><button formaction="vbscript:x">b</button></form>`,
      "steal()",
      `<head><meta http-equiv="Content-Security-Policy" content="${BOOK_DOCUMENT_CSP}"`,
    ],
  ])("sanitizes %s", (_name, type, markup, removed, kept) => {
    const result = sanitizeBookDocument(markup, type);
    expect(result).not.toContain(removed);
    expect(result).not.toContain("vbscript");
    expect(result).toContain(kept);
  });
});
