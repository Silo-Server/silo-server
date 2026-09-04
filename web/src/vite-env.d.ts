/// <reference types="vite/client" />

/** The web bundle version (web/package.json), injected by vite.config.ts. */
declare const __SILO_WEB_VERSION__: string | undefined;

declare module "foliate-js/view.js";
declare module "foliate-js/epubcfi.js";
declare module "foliate-js/comic-book.js";
declare module "foliate-js/fb2.js";
declare module "foliate-js/epub.js";
declare module "foliate-js/pdf.js";
declare module "foliate-js/mobi.js";
declare module "foliate-js/vendor/fflate.js";
