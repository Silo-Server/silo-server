import * as webStreams from "node:stream/web";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  BOOK_DOCUMENT_CSP,
  protectBookContent,
  sanitizeBookDocument,
} from "@/reader/bookContentSecurity";
import { type BookDoc, CFI, DocumentLoader } from "@/reader/readest/libs/document";

const CHAPTER = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>One</title><link rel="stylesheet" href="style.css"/><style>p { color: red; }
body > p { margin: 0; }
p::after { content: " &amp; more"; }</style>
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
<item id="c3" href="chapter3.xhtml" media-type="application/xhtml+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
<item id="js" href="app.js" media-type="application/javascript"/>
<item id="png" href="cover.png" media-type="image/png"/>
<item id="untyped" href="untyped.html"/>
</manifest>
<spine><itemref idref="c1"/><itemref idref="c2"/><itemref idref="c3"/></spine>
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
    // Not well-formed XML, so the vendored loader reads it as HTML.
    "chapter3.xhtml": `<html><head><style>body > p { margin: 0; }</style></head><body><p>Three<br></p></body></html>`,
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

// Reads `markup` the way a srcdoc frame does (as HTML) and checks the result:
// nothing that could run, and the policy first in <head>.
function expectInertFrame(markup: string) {
  const doc = new DOMParser().parseFromString(markup, "text/html");
  const roots: ParentNode[] = [doc];
  for (let root = roots.pop(); root; root = roots.pop()) {
    for (const el of Array.from(root.querySelectorAll("*"))) {
      expect(el.localName).not.toMatch(/^(?:script|noscript|set)$/);
      for (const attr of Array.from(el.attributes)) {
        expect(attr.name).not.toMatch(/^on|^srcdoc$|^shadowrootmode$/i);
        if (/^(?:href|src|action|formaction)$/.test(attr.localName)) {
          expect(attr.value.replace(/\s/g, "")).not.toMatch(/^(?:javascript|vbscript):/i);
        }
      }
      const content = el.localName === "template" ? (el as HTMLTemplateElement).content : null;
      if (content) roots.push(content);
    }
  }
  const policy = doc.head.querySelector('meta[http-equiv="Content-Security-Policy"]');
  expect(policy?.getAttribute("content")).toBe(BOOK_DOCUMENT_CSP);
}

// The text of each <style> as a frame parsing `markup` as `type` reads it,
// without CSS comments.
function inlineCSS(markup: string, type: DOMParserSupportedType): string[] {
  const doc = new DOMParser().parseFromString(markup, type);
  expect(doc.querySelector("parsererror")).toBeNull();
  return Array.from(doc.querySelectorAll("style"), (el) =>
    (el.textContent ?? "").replace(/\/\*[\s\S]*?\*\//g, "").trim(),
  );
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
    const [first, second, third] = book.sections as unknown as LoadableSection[];

    // Reflowable chapters render through srcdoc, fixed-layout ones through the
    // blob URL; both must carry the sanitized markup.
    const url = await first!.load();
    const srcdoc = await first!.loadContent();
    const blob = blobs.get(url)!;
    expect(blob.type).toBe("application/xhtml+xml");
    expect(await blob.text()).toBe(srcdoc);
    expectInertFrame(srcdoc);
    expect(srcdoc).not.toContain("noscript");
    // Ordinary book content survives.
    // Inline CSS reads the same whether the frame parses it as HTML (srcdoc)
    // or as XML (the blob a fixed-layout frame loads).
    for (const type of ["text/html", "application/xhtml+xml"] as const) {
      expect(inlineCSS(srcdoc, type)).toEqual([
        'p { color: red; }\nbody > p { margin: 0; }\np::after { content: " & more"; }',
      ]);
    }
    expect(srcdoc).toMatch(/<p id="intro" style="font-weight: bold">Chapter text<\/p>/);
    expect(srcdoc).toContain('alt="javascript: the good parts"');
    expect(srcdoc).toContain('href="chapter2.xhtml#x" epub:type="noteref"');
    expect(srcdoc).toMatch(/<link rel="stylesheet" href="blob:book\/\d+"/);
    expect(srcdoc).toMatch(/<img src="blob:book\/\d+"/);
    expect(srcdoc).toContain("<text>svg link</text>");

    // The untyped page behind the iframe is typed from its name and sanitized,
    // never left for the browser to sniff.
    const untyped = blobs.get(srcdoc.match(/<iframe src="(blob:book\/\d+)"/)![1]!)!;
    expect(untyped.type).toBe("text/html");
    expectInertFrame(await untyped.text());

    // A document type the vendored loader does not recognise is still sanitized.
    const secondBlob = blobs.get(await second!.load())!;
    expect(secondBlob.type).toBe("text/html");
    const secondMarkup = await secondBlob.text();
    expectInertFrame(secondMarkup);
    expect(secondMarkup).toContain("<p>Two</p>");

    // A chapter the loader had to read as HTML keeps its inline CSS too.
    const thirdMarkup = await third!.loadContent();
    expectInertFrame(thirdMarkup);
    expect(inlineCSS(thirdMarkup, "text/html")).toEqual(["body > p { margin: 0; }"]);
  });

  it.each([
    ["XHTML", "application/xhtml+xml", "&gt;", "&amp;", ""],
    ["XHTML in CDATA", "application/xhtml+xml", "<![CDATA[>", "&]]>", ""],
    ["HTML", "text/html", ">", "&", ""],
    // A prefixed style's rules apply too. jsdom drops the prefix on its own,
    // so the Firefox case (the prefix kept) is not reproducible here.
    ["prefixed XHTML", "application/xhtml+xml", "&gt;", "&amp;", "h:"],
  ])("keeps %s inline style rules that contain > and &", (_name, type, gt, amp, prefix) => {
    const markup = `<html xmlns="http://www.w3.org/1999/xhtml"><head xmlns:h="http://www.w3.org/1999/xhtml"><title>t</title><${prefix}style>body ${gt} p { margin: 0 } p::after { content: "${amp}" }</${prefix}style></head><body><p>x</p></body></html>`;
    const result = sanitizeBookDocument(markup, type);
    expectInertFrame(result);
    const expected = ['body > p { margin: 0 } p::after { content: "&" }'];
    expect(inlineCSS(result, "text/html")).toEqual(expected);
    if (type !== "text/html") expect(inlineCSS(result, "application/xhtml+xml")).toEqual(expected);
  });

  it("keeps a style's CSS inside an uppercase raw-text ancestor", () => {
    const markup = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body><TEXTAREA><style>p::after { content: "&lt;/textarea&gt;&lt;p id=out&gt;" }</style></TEXTAREA><p>x</p></body></html>`;
    const result = sanitizeBookDocument(markup, "application/xhtml+xml");
    expectInertFrame(result);
    const frame = new DOMParser().parseFromString(result, "text/html");
    expect(frame.querySelector("p")?.textContent).toBe("x");
    expect(frame.getElementById("out")).toBeNull();
  });

  // Each payload is XHTML that the sanitizer reads as XML but a srcdoc frame
  // reads as HTML; none may come out active.
  it.each([
    ["comment", `<p><!--><img src="x" onerror="steal()"/>--></p>`],
    ["CDATA in style", `<style><![CDATA[</style><img src=x onerror=steal()>]]></style>`],
    [
      "style end tag in CSS",
      `<style>p::after { content: "&lt;/STYLE &gt;&lt;img src=x onerror=steal()&gt;]]&gt;" }</style>`,
    ],
    ["processing instruction", `<p><?x ><img src=x onerror=steal()>?></p>`],
    [
      "template shadow root",
      `<template shadowrootmode="open"><img src="x" onerror="steal()"/></template>`,
    ],
    [
      "noscript",
      `<noscript><p title="&lt;/noscript&gt;&lt;img src=x onerror=steal()&gt;">x</p></noscript>`,
    ],
    [
      "SVG script and href animation",
      `<svg xmlns="http://www.w3.org/2000/svg"><script>steal()</script><a><set attributeName="href" to="javascript:steal()"/></a></svg>`,
    ],
    [
      "script URL",
      `<a href=" java&#x09;script:steal()">x</a><form action="vbscript:x"><button formaction="javascript:steal()">b</button></form>`,
    ],
  ])("keeps a %s inert in a srcdoc frame", (_name, body) => {
    const xhtml = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body>${body}</body></html>`;
    const result = sanitizeBookDocument(xhtml, "application/xhtml+xml");
    expectInertFrame(result);
  });

  it.each([
    [
      "head after body",
      `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>x</p></body><head><title>t</title></head></html>`,
    ],
    [
      "CSS stylesheet PI",
      `<?xml-stylesheet type="text/css" href="><img src=x onerror=steal()>"?><html xmlns="http://www.w3.org/1999/xhtml"><head/><body/></html>`,
    ],
    [
      "doctype identifier",
      `<!DOCTYPE html SYSTEM "x><img src=x onerror=steal()>"><html xmlns="http://www.w3.org/1999/xhtml"><head/><body/></html>`,
    ],
  ])("puts the policy first with a %s", (_name, xhtml) => {
    const result = sanitizeBookDocument(xhtml, "application/xhtml+xml");
    expectInertFrame(result);
    const doc = new DOMParser().parseFromString(result, "text/html");
    expect(doc.head.firstElementChild?.getAttribute("charset")).toBe("utf-8");
  });

  it("keeps a chapter whose doctype name the DOM cannot create", () => {
    const result = sanitizeBookDocument(
      "<!DOCTYPE 123><html><body><p>x</p></body></html>",
      "text/html",
    );
    expectInertFrame(result);
    expect(result).toMatch(/^<!DOCTYPE html>/);
    expect(result).toContain("<p>x</p>");
  });

  // Search and saved highlights take their locations from the unsanitized
  // chapter, so removing an element must not shift its later siblings.
  it("keeps reading locations from the unsanitized chapter valid", () => {
    const xhtml = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body>
<p>one</p><script>steal()</script><noscript><p>off</p></noscript>
<svg xmlns="http://www.w3.org/2000/svg"><set attributeName="href" to="javascript:steal()"/><text>svg</text></svg>
<p id="target">two</p></body></html>`;
    const original = new DOMParser().parseFromString(xhtml, "application/xhtml+xml");
    const target = original.createRange();
    target.selectNodeContents(original.getElementById("target")!.firstChild!);
    const svgText = original.createRange();
    svgText.selectNodeContents(original.getElementsByTagName("text")[0]!.firstChild!);

    const rendered = new DOMParser().parseFromString(
      sanitizeBookDocument(xhtml, "application/xhtml+xml"),
      "text/html",
    );
    for (const [range, text] of [
      [target, "two"],
      [svgText, "svg"],
    ] as const) {
      const resolved = CFI.toRange(rendered, CFI.parse(CFI.fromRange(range)));
      expect(resolved.toString()).toBe(text);
    }
  });

  it.each([
    [
      "a charset parameter",
      "text/html; charset=windows-1252",
      "<html><body><p>café</p></body></html>",
    ],
    [
      "an XML declaration",
      "application/xml",
      `<?xml version="1.0" encoding="ISO-8859-1"?><doc>café</doc>`,
    ],
  ])("decodes a document by %s", async (_name, type, markup) => {
    const target = new EventTarget();
    protectBookContent({ transformTarget: target } as unknown as BookDoc);
    const bytes = Uint8Array.from(markup, (c) => c.charCodeAt(0));
    const detail = { data: new Blob([bytes]) as unknown, type: type as unknown };
    target.dispatchEvent(new CustomEvent("data", { detail }));
    const result = (await detail.data) as string;
    expect(result).toContain("café");
    // The output is UTF-8; nothing may still declare another encoding.
    expect(result).not.toMatch(/ISO-8859-1|windows-1252/i);
  });

  it.each([
    ["an XSLT", `<?xml-stylesheet type="text/xsl" href="x.xsl"?><root/>`, "x.xsl"],
    [
      "a disguised XSLT",
      `<?xml-stylesheet foo="type='text/css'" type="text/xsl" href="x.xsl"?><root/>`,
      "x.xsl",
    ],
  ])("drops %s stylesheet but keeps CSS ones", (_name, xml, removed) => {
    const result = sanitizeBookDocument(
      `<?xml-stylesheet type="text/css" href="a.css"?>${xml}`,
      "application/xml",
    );
    expect(result).not.toContain(removed);
    expect(result).toContain('href="a.css"');
  });

  it.each([
    ["a type hidden in a parameter", "image/png;x=,text/html", undefined, "image/png", false],
    ["a charset parameter", "text/html; charset=shift_jis", undefined, "text/html", true],
    ["XSLT", "text/xsl", undefined, "text/xsl", true],
    ["an untyped stylesheet", undefined, "OEBPS/style.css", "text/css", false],
    ["an untyped page", "", "OEBPS/extra.html", "text/html", true],
    ["an unknown type", "unknown/unknown", "OEBPS/blob.bin", "application/octet-stream", false],
  ])("labels %s safely", async (_name, type, name, expected, sanitized) => {
    const target = new EventTarget();
    protectBookContent({ transformTarget: target } as unknown as BookDoc);
    const original = `<html><body><script>steal()</script></body></html>`;
    const detail: { data: unknown; type: unknown; name?: string } = { data: original, type, name };
    target.dispatchEvent(new CustomEvent("data", { detail }));
    expect(detail.type).toBe(expected);
    expect((await detail.data) !== original).toBe(sanitized);
  });
});
