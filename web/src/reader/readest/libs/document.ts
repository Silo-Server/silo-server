import * as epubcfi from "foliate-js/epubcfi.js";

export const CFI = epubcfi;

export type BookFormat =
  | "EPUB"
  | "PDF"
  | "MOBI"
  | "AZW"
  | "AZW3"
  | "CBZ"
  | "CBR"
  | "FB2"
  | "FBZ"
  | "MD";

export type LanguageMap = Record<string, string>;

export type Identifier = {
  scheme: string;
  value: string;
};

export type Contributor = {
  name: LanguageMap;
};

export type Collection = {
  name: string;
  position?: string;
};

export type Location = {
  current: number;
  next: number;
  total: number;
};

export interface TOCItem {
  id: number;
  label: string;
  href: string;
  index: number;
  cfi?: string;
  location?: Location;
  subitems?: TOCItem[];
}

export interface SectionFragment {
  id: string;
  href: string;
  cfi: string;
  size: number;
  linear: string;
  location?: Location;
  fragments?: Array<SectionFragment>;
}

export interface SectionItem {
  id: string;
  cfi: string;
  size: number;
  linear: string;
  href?: string;
  location?: Location;
  pageSpread?: "left" | "right" | "center" | "";
  fragments?: Array<SectionFragment>;
  loadText?: () => Promise<string | null>;
  createDocument: () => Promise<Document>;
}

export type BookMetadata = {
  title: string | LanguageMap;
  author: string | Contributor;
  language: string | string[];
  editor?: string;
  publisher?: string;
  published?: string;
  description?: string;
  subject?: string | string[] | Contributor;
  identifier?: string;
  isbn?: string;
  altIdentifier?: string | string[] | Identifier;
  belongsTo?: {
    collection?: Array<Collection> | Collection;
    series?: Array<Collection> | Collection;
  };
  subtitle?: string;
  series?: string;
  seriesIndex?: number;
  seriesTotal?: number;
  coverImageFile?: string;
  coverImageUrl?: string;
  coverImageBlobUrl?: string;
};

export interface BookDoc {
  metadata: BookMetadata;
  rendition: {
    layout?: "pre-paginated" | "reflowable";
    spread?: "auto" | "none";
    viewport?: { width: number; height: number };
  };
  dir: string;
  toc?: Array<TOCItem>;
  sections: Array<SectionItem>;
  transformTarget?: EventTarget;
  splitTOCHref(href: string): Array<string | number>;
  getCover(): Promise<Blob | null>;
}

export const EXTS: Record<BookFormat, string> = {
  EPUB: "epub",
  PDF: "pdf",
  MOBI: "mobi",
  AZW: "azw",
  AZW3: "azw3",
  CBZ: "cbz",
  CBR: "cbr",
  FB2: "fb2",
  FBZ: "fbz",
  MD: "md",
};

export const MIMETYPES: Record<BookFormat, string[]> = {
  EPUB: ["application/epub+zip"],
  PDF: ["application/pdf"],
  MOBI: ["application/x-mobipocket-ebook"],
  AZW: ["application/vnd.amazon.ebook"],
  AZW3: ["application/vnd.amazon.mobi8-ebook", "application/x-mobi8-ebook"],
  CBZ: [
    "application/vnd.comicbook+zip",
    "application/zip",
    "application/x-cbz",
  ],
  CBR: [
    "application/vnd.comicbook-rar",
    "application/x-cbr",
    "application/vnd.rar",
    "application/x-rar-compressed",
  ],
  FB2: ["application/x-fictionbook+xml", "text/xml", "application/xml"],
  FBZ: ["application/x-zip-compressed-fb2", "application/zip"],
  MD: ["text/markdown", "text/x-markdown"],
};

export class DocumentLoader {
  private file: File;

  constructor(file: File) {
    this.file = file;
  }

  private async isZip(): Promise<boolean> {
    const arr = new Uint8Array(await this.file.slice(0, 4).arrayBuffer());
    return arr[0] === 0x50 && arr[1] === 0x4b && arr[2] === 0x03;
  }

  private async isPDF(): Promise<boolean> {
    const arr = new Uint8Array(await this.file.slice(0, 5).arrayBuffer());
    return (
      arr[0] === 0x25 &&
      arr[1] === 0x50 &&
      arr[2] === 0x44 &&
      arr[3] === 0x46 &&
      arr[4] === 0x2d
    );
  }

  private async isRar(): Promise<boolean> {
    const arr = new Uint8Array(await this.file.slice(0, 8).arrayBuffer());
    return (
      arr[0] === 0x52 &&
      arr[1] === 0x61 &&
      arr[2] === 0x72 &&
      arr[3] === 0x21 &&
      arr[4] === 0x1a &&
      arr[5] === 0x07 &&
      (arr[6] === 0x00 || (arr[6] === 0x01 && arr[7] === 0x00))
    );
  }

  private async makeZipLoader() {
    const { configure, ZipReader, BlobReader, TextWriter, BlobWriter } =
      await import("@zip.js/zip.js");
    configure({ useWebWorkers: false, useCompressionStream: false });

    type Entry = import("@zip.js/zip.js").Entry;
    const reader = new ZipReader(new BlobReader(this.file));
    const entries = await reader.getEntries();
    const map = new Map(entries.map((entry) => [entry.filename, entry]));
    const lowercaseMap = new Map<string, Entry | null>();
    for (const entry of entries) {
      const lowercaseName = entry.filename.toLowerCase();
      const existing = lowercaseMap.get(lowercaseName);
      lowercaseMap.set(
        lowercaseName,
        existing && existing.filename !== entry.filename ? null : entry,
      );
    }
    const getEntry = (name: string) =>
      map.get(name) ?? lowercaseMap.get(name.toLowerCase()) ?? null;
    const load =
      (f: (entry: Entry, type?: string) => Promise<string | Blob> | null) =>
      (name: string, ...args: [string?]) => {
        const entry = getEntry(name);
        return entry ? f(entry, ...args) : null;
      };

    const loadText = load((entry: Entry) =>
      !entry.directory ? entry.getData(new TextWriter()) : null,
    );
    const loadBlob = load((entry: Entry, type?: string) =>
      !entry.directory ? entry.getData(new BlobWriter(type ?? "")) : null,
    );
    const getSize = (name: string) => getEntry(name)?.uncompressedSize ?? 0;
    const getComment = async (): Promise<string | null> => null;

    return {
      entries,
      loadText,
      loadBlob,
      getSize,
      getComment,
      sha1: undefined,
    };
  }

  private async makeRarLoader() {
    const [{ createExtractorFromData }, wasmUrl] = await Promise.all([
      import("node-unrar-js/esm/index.esm"),
      import("node-unrar-js/esm/js/unrar.wasm?url").then(
        (module) => module.default,
      ),
    ]);
    const [data, wasmBinary] = await Promise.all([
      this.file.arrayBuffer(),
      fetch(wasmUrl).then((response) => response.arrayBuffer()),
    ]);
    const extractor = await createExtractorFromData({ data, wasmBinary });
    const headers = [...extractor.getFileList().fileHeaders];
    const extracted = new Map<string, Uint8Array>();
    for (const file of extractor.extract().files) {
      if (!file.fileHeader.flags.directory && file.extraction) {
        extracted.set(file.fileHeader.name, file.extraction);
      }
    }

    const entries = headers.map((header) => ({
      directory: header.flags.directory,
      filename: header.name,
      uncompressedSize: header.unpSize,
    }));
    const lowercaseMap = new Map<string, string | null>();
    for (const entry of entries) {
      const lowercaseName = entry.filename.toLowerCase();
      const existing = lowercaseMap.get(lowercaseName);
      lowercaseMap.set(
        lowercaseName,
        existing && existing !== entry.filename ? null : entry.filename,
      );
    }
    const resolveName = (name: string) =>
      extracted.has(name) ? name : lowercaseMap.get(name.toLowerCase()) || name;
    const loadBlob = (name: string, type?: string) => {
      const data = extracted.get(resolveName(name));
      return data
        ? new Blob([new Uint8Array(data)], { type: type ?? "" })
        : null;
    };
    const loadText = async (name: string) => {
      const blob = loadBlob(name);
      return blob ? blob.text() : null;
    };
    const getSize = (name: string) => {
      const resolved = resolveName(name);
      return (
        entries.find((entry) => entry.filename === resolved)
          ?.uncompressedSize ?? 0
      );
    };

    return {
      entries,
      loadText,
      loadBlob,
      getSize,
      getComment: async (): Promise<string | null> => null,
      sha1: undefined,
    };
  }

  private isCBZ(): boolean {
    return (
      this.file.type === "application/vnd.comicbook+zip" ||
      this.file.name.toLowerCase().endsWith(`.${EXTS.CBZ}`)
    );
  }

  private isCBR(): boolean {
    const name = this.file.name.toLowerCase();
    return (
      this.file.type === "application/vnd.comicbook-rar" ||
      this.file.type === "application/x-cbr" ||
      this.file.type === "application/vnd.rar" ||
      this.file.type === "application/x-rar-compressed" ||
      name.endsWith(`.${EXTS.CBR}`) ||
      name.endsWith(".rar")
    );
  }

  private isFB2(): boolean {
    return (
      this.file.type === "application/x-fictionbook+xml" ||
      this.file.name.toLowerCase().endsWith(`.${EXTS.FB2}`)
    );
  }

  private isFBZ(): boolean {
    const name = this.file.name.toLowerCase();
    return (
      this.file.type === "application/x-zip-compressed-fb2" ||
      name.endsWith(".fb.zip") ||
      name.endsWith(".fb2.zip") ||
      name.endsWith(`.${EXTS.FBZ}`)
    );
  }

  private isPlainText(): boolean {
    const name = this.file.name.toLowerCase();
    return (
      this.file.type === "text/markdown" ||
      this.file.type === "text/x-markdown" ||
      name.endsWith(`.${EXTS.MD}`)
    );
  }

  private async makeTextBook(): Promise<BookDoc> {
    const text = await this.file.text();
    const title = this.file.name.replace(/\.[^.]+$/, "") || "Document";
    const createDocument = async () => {
      const doc = document.implementation.createHTMLDocument(title);
      const pre = doc.createElement("pre");
      pre.textContent = text;
      pre.style.whiteSpace = "pre-wrap";
      pre.style.fontFamily = "inherit";
      pre.style.lineHeight = "inherit";
      doc.body.appendChild(pre);
      return doc;
    };
    return {
      metadata: {
        title,
        author: "",
        language: "",
      },
      rendition: {
        layout: "reflowable",
        spread: "none",
      },
      dir: "",
      toc: [{ id: 0, label: title, href: "text", index: 0 }],
      sections: [
        {
          id: "text",
          href: "text",
          cfi: "",
          size: text.length,
          linear: "yes",
          createDocument,
          loadText: async () => text,
        },
      ],
      splitTOCHref: () => ["text"],
      getCover: async () => null,
    };
  }

  public async open(): Promise<{ book: BookDoc; format: BookFormat }> {
    let book: BookDoc | null = null;
    let format: BookFormat = "EPUB";
    if (!this.file.size) {
      throw new Error("File is empty");
    }
    try {
      if (await this.isZip()) {
        const loader = await this.makeZipLoader();
        const { entries } = loader;

        if (this.isCBZ()) {
          const { makeComicBook } = await import("foliate-js/comic-book.js");
          book = (await makeComicBook(loader, this.file)) as BookDoc;
          format = "CBZ";
        } else if (this.isFBZ()) {
          const entry = entries.find((item) =>
            item.filename.toLowerCase().endsWith(`.${EXTS.FB2}`),
          );
          const blob = await loader.loadBlob((entry ?? entries[0]!).filename);
          const { makeFB2 } = await import("foliate-js/fb2.js");
          book = (await makeFB2(blob)) as BookDoc;
          format = "FBZ";
        } else {
          const { EPUB } = await import("foliate-js/epub.js");
          book = (await new EPUB(loader).init()) as BookDoc;
          format = "EPUB";
        }
      } else if ((await this.isRar()) || this.isCBR()) {
        const loader = await this.makeRarLoader();
        const { makeComicBook } = await import("foliate-js/comic-book.js");
        book = (await makeComicBook(loader, this.file)) as BookDoc;
        format = "CBR";
      } else if (await this.isPDF()) {
        const { makePDF } = await import("foliate-js/pdf.js");
        book = (await makePDF(this.file)) as BookDoc;
        format = "PDF";
      } else if (await (await import("foliate-js/mobi.js")).isMOBI(this.file)) {
        const fflate = await import("foliate-js/vendor/fflate.js");
        const { MOBI } = await import("foliate-js/mobi.js");
        book = (await new MOBI({ unzlib: fflate.unzlibSync }).open(
          this.file,
        )) as BookDoc;
        const ext = this.file.name.split(".").pop()?.toLowerCase();
        switch (ext) {
          case "azw":
            format = "AZW";
            break;
          case "azw3":
            format = "AZW3";
            break;
          default:
            format = "MOBI";
        }
      } else if (this.isFB2()) {
        const { makeFB2 } = await import("foliate-js/fb2.js");
        book = (await makeFB2(this.file)) as BookDoc;
        format = "FB2";
      } else if (this.isPlainText()) {
        format = "MD";
        book = await this.makeTextBook();
      }
    } catch (e: unknown) {
      console.error("Failed to open document:", e);
      if (e instanceof Error && e.message?.includes("not a valid zip")) {
        throw new Error("Unsupported or corrupted book file");
      }
      throw e;
    }
    if (!book) {
      throw new Error("Unsupported book format");
    }
    return { book, format };
  }
}

export const getDirection = (doc: Document) => {
  const { defaultView } = doc;
  const computedStyle = defaultView!.getComputedStyle(doc.body);
  const { direction } = computedStyle;
  let { writingMode } = computedStyle;
  if (!writingMode || writingMode === "horizontal-tb") {
    const firstChild = doc.body.querySelector(":scope > :not([cfi-inert])");
    if (firstChild) {
      const childStyle = defaultView!.getComputedStyle(firstChild);
      if (
        childStyle.writingMode === "vertical-rl" ||
        childStyle.writingMode === "vertical-lr"
      ) {
        writingMode = childStyle.writingMode;
      }
    }
  }
  const vertical =
    writingMode === "vertical-rl" || writingMode === "vertical-lr";
  const rtl =
    doc.body.dir === "rtl" ||
    direction === "rtl" ||
    doc.documentElement.dir === "rtl";
  return { vertical, rtl };
};
