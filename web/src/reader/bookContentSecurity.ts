// Book documents render in iframes that share the application origin and keep
// `allow-scripts` (foliate needs it for events on WebKit), so markup from a
// hostile book would run with the signed-in user's storage. The SPA shell's CSP
// blocks that in production, but the Vite dev server sends no CSP. These hooks
// put the barrier at the book boundary instead, so it holds in every serving
// mode: scripts and other active content are removed from each document before
// it is turned into a blob or srcdoc, and every (X)HTML document carries its
// own script-blocking policy.

import type { BookDoc } from "@/reader/readest/libs/document";

export const BOOK_DOCUMENT_CSP = "script-src 'none'";

const XHTML_NS = "http://www.w3.org/1999/xhtml";

const MIME_TOKEN = /^[!#$%&'*+.^_`|~0-9a-z-]+\/[!#$%&'*+.^_`|~0-9a-z-]+$/;

// The browser sniffs these (or a missing or malformed type) when navigating, so
// HTML labelled this way would render without passing through the sanitizer.
const SNIFFED_TYPES = new Set(["unknown/unknown", "application/unknown", "*/*"]);

const URL_ATTRIBUTES = new Set([
  "action",
  "background",
  "codebase",
  "data",
  "formaction",
  "href",
  "poster",
  "src",
]);

function mimeEssence(type: unknown): string {
  return typeof type === "string" ? (type.split(";")[0] ?? "").trim().toLowerCase() : "";
}

// Types a browser renders as a script-capable document. Every XML type counts:
// XHTML-namespace <script> runs in any XML document a frame displays.
function isDocumentType(essence: string): boolean {
  return (
    essence === "text/html" ||
    essence === "text/xml" ||
    essence === "application/xml" ||
    essence.endsWith("+xml")
  );
}

function isSniffed(essence: string): boolean {
  return !MIME_TOKEN.test(essence) || SNIFFED_TYPES.has(essence);
}

function parserType(essence: string): DOMParserSupportedType {
  switch (essence) {
    case "text/html":
    case "application/xhtml+xml":
    case "image/svg+xml":
    case "text/xml":
      return essence;
    default:
      return "application/xml";
  }
}

function isScriptURL(value: string): boolean {
  // The URL parser ignores leading C0 controls and spaces and drops tabs and
  // newlines anywhere, so strip them all before reading the scheme.
  // eslint-disable-next-line no-control-regex -- matching control characters is the point
  return /^(?:javascript|vbscript):/i.test(value.replace(/[\u0000- \u007f]/g, ""));
}

function setsHref(el: Element): boolean {
  if (el.localName !== "set" && el.localName !== "animate") return false;
  const name = (el.getAttribute("attributeName") ?? "").trim().toLowerCase();
  return name === "href" || name.endsWith(":href");
}

// An XSLT stylesheet can generate script elements after sanitization.
function isXSLTStylesheet(node: Node): boolean {
  if (node.nodeType !== Node.PROCESSING_INSTRUCTION_NODE) return false;
  const { target, data } = node as ProcessingInstruction;
  return target === "xml-stylesheet" && !/type\s*=\s*["']text\/css["']/i.test(data);
}

function removeActiveContent(doc: Document) {
  for (const node of Array.from(doc.childNodes)) {
    if (isXSLTStylesheet(node)) node.remove();
  }
  for (const el of Array.from(doc.getElementsByTagName("*"))) {
    const name = el.localName.toLowerCase();
    // <noscript> is raw text when the frame parses it with scripting on, so
    // markup inside it could parse differently there than it did here. Its
    // content never renders in a scripting-enabled frame anyway.
    if (name === "script" || name === "noscript" || setsHref(el)) {
      el.remove();
      continue;
    }
    for (const attr of Array.from(el.attributes)) {
      const attrName = attr.localName.toLowerCase();
      if (
        attrName.startsWith("on") ||
        attrName === "srcdoc" ||
        (URL_ATTRIBUTES.has(attrName) && isScriptURL(attr.value))
      ) {
        el.removeAttributeNode(attr);
      }
    }
  }
}

function addDocumentPolicy(doc: Document) {
  const root = doc.documentElement;
  if (!root || root.namespaceURI !== XHTML_NS) return;
  let head = doc.head;
  if (!head) {
    head = doc.createElementNS(XHTML_NS, "head");
    root.prepend(head);
  }
  const meta = doc.createElementNS(XHTML_NS, "meta");
  meta.setAttribute("http-equiv", "Content-Security-Policy");
  meta.setAttribute("content", BOOK_DOCUMENT_CSP);
  head.prepend(meta);
}

/**
 * Returns `markup` with scripts, event handlers, script URLs, and other active
 * content removed, plus a script-blocking CSP when it is an (X)HTML document.
 * The output is always a serialization of the sanitized tree, never the input.
 */
export function sanitizeBookDocument(markup: string, type: string): string {
  const essence = mimeEssence(type);
  const doc = new DOMParser().parseFromString(markup, parserType(essence));
  removeActiveContent(doc);
  addDocumentPolicy(doc);
  return new XMLSerializer().serializeToString(doc);
}

type DataDetail = { data: unknown; type: unknown };
type LoadDetail = { isScript?: boolean; allow?: boolean };

async function toText(data: unknown): Promise<string> {
  if (typeof data === "string") return data;
  // Loaders hand over a Blob, which may come from another realm.
  if (typeof (data as Blob | null)?.text === "function") return (data as Blob).text();
  return new Blob([data as BlobPart]).text();
}

/**
 * Sanitizes every document a book's loader turns into a blob or srcdoc. Must
 * run before the first section loads. EPUB (native and server-converted) and
 * MOBI/KF8 expose `transformTarget`; FB2, comics, PDF, and plain text build
 * their own markup from a fixed set of elements and have none.
 */
export function protectBookContent(book: BookDoc) {
  const target = book.transformTarget;
  if (!target) return;
  target.addEventListener("load", (event) => {
    const detail = (event as CustomEvent<LoadDetail>).detail;
    if (detail.isScript) detail.allow = false;
  });
  target.addEventListener("data", (event) => {
    const detail = (event as CustomEvent<DataDetail>).detail;
    const essence = mimeEssence(detail.type);
    if (isDocumentType(essence)) {
      detail.data = Promise.resolve(detail.data).then(async (data) =>
        sanitizeBookDocument(await toText(data), essence),
      );
    } else if (isSniffed(essence)) {
      // Images, fonts, and media still load as octet-stream; a frame does not
      // render it as a document.
      detail.type = "application/octet-stream";
    }
  });
}
