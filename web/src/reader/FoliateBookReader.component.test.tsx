// @vitest-environment jsdom

import { act, createRef, type Ref } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion } from "@/api/types";
import type { BookDoc } from "@/reader/readest/libs/document";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  apiBlob: vi.fn(),
  apiKeepalive: vi.fn(),
  loaderOpen: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
  apiBlob: mocks.apiBlob,
  apiKeepalive: mocks.apiKeepalive,
}));

vi.mock("@/reader/readest/libs/document", async () => {
  const actual = await vi.importActual<typeof import("@/reader/readest/libs/document")>(
    "@/reader/readest/libs/document",
  );
  return {
    ...actual,
    DocumentLoader: class {
      private file: File;

      constructor(file: File) {
        this.file = file;
      }

      open() {
        return mocks.loaderOpen(this.file);
      }
    },
  };
});

vi.mock("foliate-js/view.js", () => ({}));

import FoliateBookReader, {
  ebookReaderProgressQueryKey,
  type FoliateBookReaderHandle,
  type ReaderLocationInfo,
  type ReaderReadyState,
} from "@/reader/FoliateBookReader";

type DestroyableBook = BookDoc & { destroy: () => void };

const createdViews: FakeFoliateView[] = [];
// Each created view shifts one behavior off this queue for its open() call;
// when the queue is empty open() resolves immediately.
const viewOpenBehaviors: Array<() => Promise<void>> = [];

class FakeFoliateView extends HTMLElement {
  book: unknown = null;
  close = vi.fn();
  init = vi.fn(async () => {});
  goToFraction = vi.fn(async () => {});
  getSectionFractions = vi.fn((): number[] => []);
  renderer: { getContents?: () => Array<{ doc: Document; index?: number }> } | undefined =
    undefined;
  open = vi.fn((book: unknown) => {
    this.book = book;
    const behavior = viewOpenBehaviors.shift();
    return behavior ? behavior() : Promise.resolve();
  });

  constructor() {
    super();
    createdViews.push(this);
  }
}

if (!customElements.get("foliate-view")) {
  customElements.define("foliate-view", FakeFoliateView);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeBook(label: string): DestroyableBook {
  return {
    toc: [{ id: 1, label, href: `${label}.xhtml` }],
    destroy: vi.fn(),
  } as unknown as DestroyableBook;
}

function makeFile(fileID: number, name: string): FileVersion {
  return {
    file_id: fileID,
    file_name: name,
    file_path: `/books/${name}`,
    resolution: "",
    codec_video: "",
    codec_audio: "",
    hdr: false,
    container: "epub",
    file_size: 1,
    duration: 0,
    bitrate: 0,
  };
}

function viewAt(index: number): FakeFoliateView {
  const view = createdViews[index];
  if (!view) throw new Error(`expected a foliate-view at index ${index}`);
  return view;
}

// Builds a standalone Document (not attached to any real iframe) standing in
// for one of foliate's content docs, with `defaultView.frameElement` stubbed
// to report the given frame offset — mirroring the real relationship between
// a content doc and the same-origin iframe foliate renders it into.
function makeFakeContentDoc(frameRect: { left: number; top: number }): Document {
  const doc = document.implementation.createHTMLDocument("content");
  const frameElement = document.createElement("div");
  frameElement.getBoundingClientRect = () =>
    ({
      left: frameRect.left,
      top: frameRect.top,
      width: 0,
      height: 0,
      right: frameRect.left,
      bottom: frameRect.top,
      x: frameRect.left,
      y: frameRect.top,
      toJSON() {
        return {};
      },
    }) as DOMRect;
  Object.defineProperty(doc, "defaultView", {
    configurable: true,
    value: { frameElement },
  });
  return doc;
}

function relocateEvent(current: number, cfi?: string) {
  return new CustomEvent("relocate", {
    detail: { cfi, location: { current, total: 10 } },
  });
}

describe("FoliateBookReader open flow", () => {
  let container: HTMLDivElement;
  let root: Root;
  let queryClient: QueryClient;

  const fileA = makeFile(7, "a.epub");
  const fileB = makeFile(8, "b.epub");

  function ui(
    file: FileVersion,
    props: {
      onReady?: (state: ReaderReadyState) => void;
      onLocationChange?: (info: ReaderLocationInfo) => void;
      onContentPointerUp?: (point: { clientX: number; clientY: number }) => void;
      onContentPointerMove?: (point: { clientY: number }) => void;
    } = {},
    ref?: Ref<FoliateBookReaderHandle>,
  ) {
    return (
      <QueryClientProvider client={queryClient}>
        <FoliateBookReader contentID="book-1" file={file} title="Book" {...props} ref={ref} />
      </QueryClientProvider>
    );
  }

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    queryClient = new QueryClient();
    createdViews.length = 0;
    viewOpenBehaviors.length = 0;
    mocks.api.mockReset();
    mocks.apiBlob.mockReset();
    mocks.apiKeepalive.mockReset();
    mocks.loaderOpen.mockReset();
    mocks.api.mockImplementation(async (_path: string, options?: RequestInit) => {
      if (options?.method === "PUT") {
        return JSON.parse(String(options.body)) as Record<string, unknown>;
      }
      return {};
    });
    mocks.apiBlob.mockResolvedValue(new Blob(["epub"], { type: "application/epub+zip" }));
    let urlCounter = 0;
    URL.createObjectURL = vi.fn(() => `blob:mock-${++urlCounter}`);
    URL.revokeObjectURL = vi.fn();
  });

  afterEach(async () => {
    vi.useRealTimers();
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  it("tears down a superseded run when the loader resolves after a file switch", async () => {
    const bookA = makeBook("A");
    const bookB = makeBook("B");
    const loaderA = deferred<{ book: BookDoc }>();
    mocks.loaderOpen.mockImplementation((file: File) =>
      file.name === "a.epub" ? loaderA.promise : Promise.resolve({ book: bookB }),
    );
    const onReady = vi.fn();

    await act(async () => {
      root.render(ui(fileA, { onReady }));
    });
    // Run A is parked on DocumentLoader.open(); switch files before it resolves.
    await act(async () => {
      root.render(ui(fileB, { onReady }));
    });
    await act(async () => {
      loaderA.resolve({ book: bookA });
    });

    expect(bookA.destroy).toHaveBeenCalledTimes(1);
    // The stale run must not create a view or report a stale TOC.
    expect(createdViews).toHaveLength(1);
    expect(container.querySelectorAll("foliate-view")).toHaveLength(1);
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady).toHaveBeenCalledWith({ toc: bookB.toc });
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:mock-1");
  });

  it("closes a stale view whose open() resolves after cancellation and keeps the live one", async () => {
    const bookA = makeBook("A");
    const bookB = makeBook("B");
    mocks.loaderOpen.mockImplementation((file: File) =>
      Promise.resolve({ book: file.name === "a.epub" ? bookA : bookB }),
    );
    const viewOpenA = deferred<void>();
    viewOpenBehaviors.push(() => viewOpenA.promise);
    const onReady = vi.fn();

    await act(async () => {
      root.render(ui(fileA, { onReady }));
    });
    expect(createdViews).toHaveLength(1);
    const viewA = viewAt(0);

    await act(async () => {
      root.render(ui(fileB, { onReady }));
    });
    expect(createdViews).toHaveLength(2);
    const viewB = viewAt(1);

    await act(async () => {
      viewOpenA.resolve();
    });

    expect(viewA.close).toHaveBeenCalled();
    expect(viewA.isConnected).toBe(false);
    expect(bookA.destroy).toHaveBeenCalledTimes(1);
    expect(bookB.destroy).not.toHaveBeenCalled();
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady).toHaveBeenCalledWith({ toc: bookB.toc });
    expect(Array.from(container.querySelectorAll("foliate-view"))).toEqual([viewB]);

    // Relocates from the superseded view must not save progress for its file.
    await act(async () => {
      viewA.dispatchEvent(relocateEvent(3, "epubcfi(/6/2)"));
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(mocks.apiKeepalive).not.toHaveBeenCalled();
  });

  it("flushes pending progress with a keepalive request when the page hides", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });

    await act(async () => {
      root.render(ui(fileA));
    });
    const view = viewAt(0);

    await act(async () => {
      view.dispatchEvent(relocateEvent(2, "epubcfi(/6/4)"));
    });
    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });

    expect(mocks.apiKeepalive).toHaveBeenCalledTimes(1);
    expect(mocks.apiKeepalive).toHaveBeenCalledWith("/ebooks/book-1/progress", {
      method: "PUT",
      body: JSON.stringify({ file_id: 7, location: "epubcfi(/6/4)", progress: 0.3 }),
    });

    // The pending save was consumed; hiding again must not re-send it.
    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(mocks.apiKeepalive).toHaveBeenCalledTimes(1);
  });

  it("flushes pending progress through the authenticated api when the tab merely hides", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });

    await act(async () => {
      root.render(ui(fileA));
    });
    const view = viewAt(0);

    await act(async () => {
      view.dispatchEvent(relocateEvent(2, "epubcfi(/6/4)"));
    });

    const putCalls = () =>
      mocks.api.mock.calls.filter(([, options]) => (options as RequestInit)?.method === "PUT");
    expect(putCalls()).toHaveLength(0);

    // The page is alive while hidden; the normal api path can refresh an
    // expired token, unlike keepalive.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    try {
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });

      expect(mocks.apiKeepalive).not.toHaveBeenCalled();
      expect(putCalls()).toHaveLength(1);
      expect(putCalls()[0]).toEqual([
        "/ebooks/book-1/progress",
        {
          method: "PUT",
          body: JSON.stringify({ file_id: 7, location: "epubcfi(/6/4)", progress: 0.3 }),
        },
      ]);

      // The pending save was consumed once; a pagehide with no new progress
      // must not double-send it through keepalive.
      await act(async () => {
        window.dispatchEvent(new Event("pagehide"));
      });
      expect(mocks.apiKeepalive).not.toHaveBeenCalled();
      expect(putCalls()).toHaveLength(1);
    } finally {
      Object.defineProperty(document, "visibilityState", {
        configurable: true,
        get: () => "visible",
      });
    }
  });

  it("opens external http(s) links itself with noopener and blocks unsafe schemes", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const open = vi.fn();
    vi.stubGlobal("open", open);

    try {
      await act(async () => {
        root.render(ui(fileA));
      });
      const view = viewAt(0);

      // Mirrors the vendored foliate contract: it emits a cancelable
      // 'external-link' event and only runs its own globalThis.open(href,
      // '_blank') when dispatchEvent returns true (no preventDefault).
      const externalLink = (href: string) =>
        new CustomEvent("external-link", { detail: { href }, cancelable: true });

      const httpsEvent = externalLink("https://example.com/page");
      let defaultAllowed = true;
      await act(async () => {
        defaultAllowed = view.dispatchEvent(httpsEvent);
      });
      // The vendor default open must be cancelled; we open safely ourselves.
      expect(defaultAllowed).toBe(false);
      expect(open).toHaveBeenCalledTimes(1);
      expect(open).toHaveBeenCalledWith(
        "https://example.com/page",
        "_blank",
        "noopener,noreferrer",
      );

      for (const href of ["javascript:alert(1)", "data:text/html,hi", "not a url"]) {
        let allowed = true;
        await act(async () => {
          allowed = view.dispatchEvent(externalLink(href));
        });
        expect(allowed).toBe(false);
      }
      expect(open).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("surfaces a user-facing error when the ebook file fails to load", async () => {
    mocks.apiBlob.mockRejectedValue(
      new Error("This file is too large to open in the browser (3072 MiB, limit 512 MiB)."),
    );

    await act(async () => {
      root.render(ui(fileA));
    });

    expect(container.textContent).toContain("This file is too large to open in the browser");
    expect(container.textContent).not.toContain("Loading reader...");
  });

  it("ignores progress save responses that resolve out of order", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const firstPut = deferred<Record<string, unknown>>();
    const secondPut = deferred<Record<string, unknown>>();
    const puts = [firstPut, secondPut];
    mocks.api.mockImplementation((_path: string, options?: RequestInit) => {
      if (options?.method === "PUT") {
        return puts.shift()!.promise;
      }
      return Promise.resolve({});
    });

    await act(async () => {
      root.render(ui(fileA));
    });
    const view = viewAt(0);
    vi.useFakeTimers();

    await act(async () => {
      view.dispatchEvent(relocateEvent(1, "epubcfi(/6/2)"));
      vi.advanceTimersByTime(800);
    });
    await act(async () => {
      view.dispatchEvent(relocateEvent(4, "epubcfi(/6/8)"));
      vi.advanceTimersByTime(800);
    });

    // The newer save resolves first; the older response must not overwrite it.
    await act(async () => {
      secondPut.resolve({ file_id: 7, location: "epubcfi(/6/8)", progress: 0.5 });
    });
    await act(async () => {
      firstPut.resolve({ file_id: 7, location: "epubcfi(/6/2)", progress: 0.2 });
    });

    expect(queryClient.getQueryData(ebookReaderProgressQueryKey("book-1"))).toMatchObject({
      location: "epubcfi(/6/8)",
      progress: 0.5,
    });
  });

  it("reports location info on relocate", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const onLocationChange = vi.fn();

    await act(async () => {
      root.render(ui(fileA, { onLocationChange }));
    });
    const view = viewAt(0);

    await act(async () => {
      view.dispatchEvent(
        new CustomEvent("relocate", {
          detail: {
            cfi: "epubcfi(/6/8!/4/2)",
            fraction: 0.3,
            index: 1,
            tocItem: { label: "Chapter 2" },
            location: { current: 29, total: 100 },
          },
        }),
      );
    });

    expect(onLocationChange).toHaveBeenCalledWith({
      fraction: 0.3,
      sectionIndex: 1,
      tocLabel: "Chapter 2",
    });
  });

  it("ignores a relocate event reporting a non-finite fraction", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const onLocationChange = vi.fn();

    await act(async () => {
      root.render(ui(fileA, { onLocationChange }));
    });
    const view = viewAt(0);

    await act(async () => {
      view.dispatchEvent(
        new CustomEvent("relocate", {
          detail: {
            cfi: "epubcfi(/6/8!/4/2)",
            fraction: Number.NaN,
            index: 1,
            tocItem: { label: "Chapter 2" },
            location: { current: 29, total: 100 },
          },
        }),
      );
    });

    expect(onLocationChange).not.toHaveBeenCalled();
  });

  it("exposes section fractions through the handle", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const handleRef = createRef<FoliateBookReaderHandle>();

    await act(async () => {
      root.render(ui(fileA, {}, handleRef));
    });
    const view = viewAt(0);
    view.getSectionFractions = vi.fn(() => [0, 0.25, 1]);

    expect(handleRef.current?.getSectionFractions()).toEqual([0, 0.25, 1]);
  });

  it("forwards pointerup and mousemove from content iframes with viewport-adjusted coordinates", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const onContentPointerUp = vi.fn();
    const onContentPointerMove = vi.fn();

    await act(async () => {
      root.render(ui(fileA, { onContentPointerUp, onContentPointerMove }));
    });
    const view = viewAt(0);

    // foliate re-creates content docs and fires "create-overlay" for each;
    // the frame sits at (40, 20) in outer viewport coordinates.
    const contentDoc = makeFakeContentDoc({ left: 40, top: 20 });
    view.renderer = { getContents: () => [{ doc: contentDoc, index: 0 }] };

    await act(async () => {
      view.dispatchEvent(new CustomEvent("create-overlay"));
    });

    await act(async () => {
      contentDoc.dispatchEvent(new MouseEvent("pointerup", { clientX: 10, clientY: 5 }));
    });
    // iframe-local (10, 5) + frame offset (40, 20) => outer viewport (50, 25).
    expect(onContentPointerUp).toHaveBeenCalledWith({ clientX: 50, clientY: 25 });

    await act(async () => {
      contentDoc.dispatchEvent(new MouseEvent("mousemove", { clientY: 5 }));
    });
    expect(onContentPointerMove).toHaveBeenCalledWith({ clientY: 25 });
  });

  it("updates hasLiveSelection synchronously, ahead of the deferred selection-change callback", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });
    const handleRef = createRef<FoliateBookReaderHandle>();

    await act(async () => {
      root.render(ui(fileA, {}, handleRef));
    });
    const view = viewAt(0);

    const contentDoc = makeFakeContentDoc({ left: 0, top: 0 });
    let fakeSelection: { isCollapsed: boolean; rangeCount: number; toString: () => string } = {
      isCollapsed: true,
      rangeCount: 0,
      toString: () => "",
    };
    contentDoc.getSelection = () => fakeSelection as unknown as Selection;
    view.renderer = { getContents: () => [{ doc: contentDoc, index: 0 }] };

    await act(async () => {
      view.dispatchEvent(new CustomEvent("create-overlay"));
    });
    expect(handleRef.current?.hasLiveSelection()).toBe(false);

    fakeSelection = { isCollapsed: false, rangeCount: 1, toString: () => "chosen text" };
    act(() => {
      contentDoc.dispatchEvent(new Event("selectionchange"));
    });
    // True immediately after the synchronous dispatch — this assertion runs
    // before the listener's setTimeout(0) deferral could ever have fired, so
    // it proves the ref updates synchronously rather than relying on that
    // deferred emitSelectionChange callback.
    expect(handleRef.current?.hasLiveSelection()).toBe(true);

    fakeSelection = { isCollapsed: true, rangeCount: 0, toString: () => "" };
    act(() => {
      contentDoc.dispatchEvent(new Event("selectionchange"));
    });
    expect(handleRef.current?.hasLiveSelection()).toBe(false);
  });

  it("does not forward content pointer events when the callbacks are absent", async () => {
    const book = makeBook("A");
    mocks.loaderOpen.mockResolvedValue({ book });

    await act(async () => {
      root.render(ui(fileA));
    });
    const view = viewAt(0);

    const contentDoc = makeFakeContentDoc({ left: 0, top: 0 });
    view.renderer = { getContents: () => [{ doc: contentDoc, index: 0 }] };

    await act(async () => {
      view.dispatchEvent(new CustomEvent("create-overlay"));
    });

    // No onContentPointerUp/onContentPointerMove prop was supplied; dispatching
    // must be a silent no-op rather than throwing.
    expect(() => {
      contentDoc.dispatchEvent(new MouseEvent("pointerup", { clientX: 10, clientY: 5 }));
      contentDoc.dispatchEvent(new MouseEvent("mousemove", { clientY: 5 }));
    }).not.toThrow();
  });
});
