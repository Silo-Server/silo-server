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

// Resource types a frame never renders as a script-capable document. Anything
// else that is not sanitized as a document is relabelled octet-stream.
const PASSIVE_TYPE =
  /^(?:image\/(?!svg)|audio\/|video\/|font\/|text\/(?:css|plain|vtt)$|application\/(?:octet-stream|pdf|font-[\w.+-]+|x-font-[\w.+-]+|vnd\.ms-opentype)$)/;

// Types inferred from the file name when a resource has no usable type.
const TYPE_BY_EXTENSION: Record<string, string> = {
  css: "text/css",
  htm: "text/html",
  html: "text/html",
  svg: "image/svg+xml",
  xhtml: "application/xhtml+xml",
};

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

// Types a browser may render as a script-capable document. Every XML type
// counts (XHTML-namespace <script> runs in any XML document a frame displays),
// and so does XSLT.
function isDocumentType(essence: string): boolean {
  return /html|xml|xsl/.test(essence);
}

// Foliate's paginator renders these through srcdoc, which parses as HTML.
function rendersAsHTML(essence: string): boolean {
  return essence === "text/html" || essence === "application/xhtml+xml";
}

function typeFromName(name: unknown): string {
  const ext = typeof name === "string" ? (name.split(".").pop() ?? "").toLowerCase() : "";
  return TYPE_BY_EXTENSION[ext] ?? "";
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

function isActiveAttribute(attr: Attr): boolean {
  const name = attr.localName.toLowerCase();
  return (
    name.startsWith("on") ||
    name === "srcdoc" ||
    // A declarative shadow root attaches when the frame parses the markup.
    name === "shadowrootmode" ||
    (URL_ATTRIBUTES.has(name) && isScriptURL(attr.value))
  );
}

function setsHref(el: Element): boolean {
  if (el.localName !== "set" && el.localName !== "animate") return false;
  const name = (el.getAttribute("attributeName") ?? "").trim().toLowerCase();
  return name === "href" || name.endsWith(":href");
}

// Keeps an `xml-stylesheet` instruction only when it is plainly CSS. Anything
// else may be XSLT, which can generate script after sanitization.
function isCSSStylesheet(pi: ProcessingInstruction): boolean {
  if (pi.target !== "xml-stylesheet" || /[<>]/.test(pi.data)) return false;
  const pseudo = /\s*([\w:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/y;
  const types: string[] = [];
  let end = 0;
  for (let match = pseudo.exec(pi.data); match; match = pseudo.exec(pi.data)) {
    end = pseudo.lastIndex;
    if (match[1] === "type") types.push(match[2] ?? match[3] ?? "");
  }
  return pi.data.slice(end).trim() === "" && types.length === 1 && types[0] === "text/css";
}

function isActiveElement(el: Element): boolean {
  const name = el.localName.toLowerCase();
  // <noscript> is raw text when the frame parses it with scripting on, so
  // markup inside it could parse differently there than it did here. Its
  // content never renders in a scripting-enabled frame anyway.
  return name === "script" || name === "noscript" || setsHref(el);
}

// An HTML-parsed <template> keeps its children in a separate fragment.
function templateContent(el: Element): DocumentFragment | null {
  return el.localName === "template" ? (el as HTMLTemplateElement).content : null;
}

// Strips active attributes from `el`; returns true when the whole element must go.
function stripElement(el: Element): boolean {
  if (isActiveElement(el)) return true;
  for (const attr of Array.from(el.attributes)) {
    if (isActiveAttribute(attr)) el.removeAttributeNode(attr);
  }
  const content = templateContent(el);
  if (content) removeActiveContent(content);
  return false;
}

function removeActiveContent(root: Document | DocumentFragment) {
  // Chapters sanitized as XML may render through srcdoc, which parses them as
  // HTML. Comments, CDATA sections, and processing instructions read
  // differently there and could carry markup past this pass, so they go.
  const doc = root.nodeType === Node.DOCUMENT_NODE ? (root as Document) : root.ownerDocument!;
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_ALL);
  const removals: Node[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    switch (node.nodeType) {
      case Node.ELEMENT_NODE:
        if (stripElement(node as Element)) removals.push(node);
        break;
      case Node.CDATA_SECTION_NODE:
        node.parentNode?.insertBefore(doc.createTextNode(node.nodeValue ?? ""), node);
        removals.push(node);
        break;
      case Node.PROCESSING_INSTRUCTION_NODE:
        if (!isCSSStylesheet(node as ProcessingInstruction)) removals.push(node);
        break;
      case Node.COMMENT_NODE:
        removals.push(node);
        break;
    }
  }
  for (const node of removals) {
    // A removed element leaves an empty <template>, which renders nothing, so
    // later siblings keep their positions. Reading locations (EPUB CFIs) count
    // those positions, and search and saved highlights compute them from the
    // unsanitized chapter.
    if (node.nodeType === Node.ELEMENT_NODE) {
      node.parentNode?.replaceChild(doc.createElementNS(XHTML_NS, "template"), node);
    } else {
      node.parentNode?.removeChild(node);
    }
  }
}

// The serializer writes a doctype's public and system identifiers without
// escaping them, so a quote in one could end the doctype and start markup.
// Only the name is kept (entities are already expanded). (X)HTML documents get
// `html`, since an HTML parser reads any other name as quirks mode. A name the
// HTML parser accepted but the DOM cannot create drops the doctype.
function replaceDoctype(doc: Document) {
  const { doctype } = doc;
  if (!doctype) return;
  const name = doc.documentElement?.namespaceURI === XHTML_NS ? "html" : doctype.name;
  try {
    doctype.replaceWith(doc.implementation.createDocumentType(name, "", ""));
  } catch {
    doctype.remove();
  }
}

function addDocumentPolicy(doc: Document) {
  const root = doc.documentElement;
  if (!root || root.namespaceURI !== XHTML_NS) return;
  const head = doc.head ?? doc.createElementNS(XHTML_NS, "head");
  // A head after the body would put the policy after the content it guards.
  root.prepend(head);
  // The markup is re-encoded as UTF-8, so drop any other declared encoding.
  for (const meta of Array.from(doc.getElementsByTagNameNS(XHTML_NS, "meta"))) {
    if (
      meta.hasAttribute("charset") ||
      meta.getAttribute("http-equiv")?.toLowerCase() === "content-type"
    ) {
      meta.remove();
    }
  }
  const charset = doc.createElementNS(XHTML_NS, "meta");
  charset.setAttribute("charset", "utf-8");
  const policy = doc.createElementNS(XHTML_NS, "meta");
  policy.setAttribute("http-equiv", "Content-Security-Policy");
  policy.setAttribute("content", BOOK_DOCUMENT_CSP);
  head.prepend(charset, policy);
}

// Checks how a frame will read the sanitized markup, so a parse or serializer
// differential fails closed instead of reaching the frame.
function isInert(markup: string, type: DOMParserSupportedType, requirePolicy: boolean): boolean {
  const doc = new DOMParser().parseFromString(markup, type);
  const policy = doc.head?.querySelector('meta[http-equiv="Content-Security-Policy"]');
  if (requirePolicy && policy?.getAttribute("content") !== BOOK_DOCUMENT_CSP) return false;
  const pending: ParentNode[] = [doc];
  for (let root = pending.pop(); root; root = pending.pop()) {
    for (const el of Array.from(root.querySelectorAll("*"))) {
      if (isActiveElement(el) || Array.from(el.attributes).some(isActiveAttribute)) return false;
      const content = templateContent(el);
      if (content) pending.push(content);
    }
  }
  return true;
}

// Elements inside which an HTML parser does not read a <style> start tag as
// the start of raw-text CSS. An HTML parser lowercases tag names, so an XHTML
// <TITLE> is a title there.
const NON_STYLE_CONTEXT = new Set([
  "frameset",
  "iframe",
  "noembed",
  "noframes",
  "noscript",
  "optgroup",
  "option",
  "plaintext",
  "script",
  "select",
  "style",
  "textarea",
  "title",
  "xmp",
]);

function isRawTextStyle(el: Element): boolean {
  for (let parent = el.parentElement; parent; parent = parent.parentElement) {
    if (parent.namespaceURI !== XHTML_NS || NON_STYLE_CONTEXT.has(parent.localName.toLowerCase())) {
      return false;
    }
  }
  return true;
}

// The serializer escapes `>` and `&` in text, which an XML parser decodes but
// an HTML parser keeps inside <style> (raw text), so `body > p` would reach
// the frame as `body &gt; p` and the rule would be dropped. Frames read the
// markup as HTML (srcdoc) or XML (a blob), so each style's CSS is written
// unescaped inside `/*<![CDATA[*/ … /*]]>*/`, which both parsers read as the
// same CSS. CSS that could end either the CDATA section or the HTML raw text
// early keeps the escaped form. An HTML parser reads a prefixed `<h:style>`
// as an unknown element, and some serializers keep the prefix, so such a
// style is first replaced by an unprefixed one.
function serializeDocument(doc: Document): string | null {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  const token = `silo-style-${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
  const styles: string[] = [];
  for (let el of Array.from(doc.getElementsByTagNameNS(XHTML_NS, "style"))) {
    const css = el.textContent ?? "";
    if (!isRawTextStyle(el) || /\]\]>|<\/style/i.test(css)) continue;
    if (el.prefix) {
      const style = doc.createElementNS(XHTML_NS, "style");
      for (const attr of Array.from(el.attributes)) {
        style.setAttributeNS(attr.namespaceURI, attr.name, attr.value);
      }
      el.replaceWith(style);
      el = style;
    }
    el.textContent = `${token}-${styles.length}-`;
    styles.push(`/*<![CDATA[*/${css}\n/*]]>*/`);
  }
  const markup = new XMLSerializer().serializeToString(doc);
  // Each placeholder must close an unprefixed <style> (a serializer may write
  // a prefix the DOM does not report) and appear once, in order, or the
  // markup is not used.
  const pieces = markup.split(new RegExp(`${token}-(\\d+)-(?=</style>)`));
  if (pieces.length !== styles.length * 2 + 1) return null;
  let result = "";
  for (const [i, piece] of pieces.entries()) {
    if (i % 2 === 0) result += piece;
    else if (Number(piece) === (i - 1) / 2) result += styles[Number(piece)];
    else return null;
  }
  return result;
}

const EMPTY_XHTML =
  `<html xmlns="${XHTML_NS}"><head><meta charset="utf-8"/>` +
  `<meta http-equiv="Content-Security-Policy" content="${BOOK_DOCUMENT_CSP}"/></head><body></body></html>`;

/**
 * Returns `markup` with scripts, event handlers, script URLs, and other active
 * content removed, plus a script-blocking CSP when it is an (X)HTML document.
 * The output is always a serialization of the sanitized tree, never the input.
 */
export function sanitizeBookDocument(markup: string, type: string): string {
  const essence = mimeEssence(type);
  const doc = new DOMParser().parseFromString(markup, parserType(essence));
  removeActiveContent(doc);
  replaceDoctype(doc);
  addDocumentPolicy(doc);
  // The markup is re-encoded as UTF-8; drop a declaration that names another
  // encoding, which a frame rendering it as XML would honour.
  const result = serializeDocument(doc)?.replace(/^<\?xml\s[^>]*\?>/, "");
  const isXHTML = doc.documentElement?.namespaceURI === XHTML_NS;
  const asHTML = rendersAsHTML(essence);
  if (
    result === undefined ||
    ((isXHTML || asHTML) && !isInert(result, "text/html", isXHTML)) ||
    // Other XML types render from a blob, through an XML parser.
    (!asHTML && !isInert(result, parserType(essence), false))
  ) {
    console.warn("Book document could not be rendered safely; showing it empty");
    return EMPTY_XHTML;
  }
  return result;
}

type DataDetail = { data: unknown; type: unknown; name?: unknown };
type LoadDetail = { isScript?: boolean; allow?: boolean };

// The encoding a frame would have used for these bytes: a byte order mark, then
// the type's charset, then an XML declaration's encoding, then UTF-8.
function encodingOf(bytes: Uint8Array, type: unknown): string {
  if (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) return "utf-8";
  if (bytes[0] === 0xfe && bytes[1] === 0xff) return "utf-16be";
  if (bytes[0] === 0xff && bytes[1] === 0xfe) return "utf-16le";
  const charset = typeof type === "string" ? /;\s*charset\s*=\s*"?([\w.:-]+)/i.exec(type) : null;
  const prolog = String.fromCharCode(...bytes.subarray(0, 128));
  const declared = /^<\?xml\s[^>]*?encoding\s*=\s*["']([\w.:-]+)["']/.exec(prolog);
  return charset?.[1] ?? declared?.[1] ?? "utf-8";
}

async function toText(data: unknown, type: unknown): Promise<string> {
  if (typeof data === "string") return data;
  // Loaders hand over a Blob, which may come from another realm, or bytes.
  const buffer =
    typeof (data as Blob | null)?.arrayBuffer === "function"
      ? await (data as Blob).arrayBuffer()
      : await new Blob([data as BlobPart]).arrayBuffer();
  const bytes = new Uint8Array(buffer);
  try {
    return new TextDecoder(encodingOf(bytes, type)).decode(bytes);
  } catch {
    // An encoding label the browser does not know.
    return new TextDecoder().decode(bytes);
  }
}

/**
 * Sanitizes every document a book's loader turns into a blob or srcdoc. Must
 * run before the first section loads. EPUB (native and server-converted) and
 * MOBI/KF8 expose `transformTarget`. FB2, comics, PDF, and plain text have
 * none: they build markup from a fixed set of elements, and the one
 * book-controlled URL among them, an FB2 link target, is never navigated
 * because foliate's view handles every link click itself.
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
    let essence = mimeEssence(detail.type);
    if (!MIME_TOKEN.test(essence)) essence = typeFromName(detail.name);
    // Only the bare type goes on: parameters can carry a second type a browser
    // would honour, or a charset the re-encoded markup no longer has.
    if (isDocumentType(essence)) {
      const declaredType = detail.type;
      detail.type = essence;
      detail.data = Promise.resolve(detail.data).then(async (data) =>
        sanitizeBookDocument(await toText(data, declaredType), essence),
      );
    } else {
      // Images, fonts, and media still load as octet-stream; a frame does not
      // render it as a document.
      detail.type = PASSIVE_TYPE.test(essence) ? essence : "application/octet-stream";
    }
  });
}
