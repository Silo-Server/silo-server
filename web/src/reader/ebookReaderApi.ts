export {
  createEbookReaderConfigSession,
  fetchEbookReaderConfig,
  saveEbookReaderConfig,
  saveEbookReaderConfigKeepalive,
  type EbookReaderConfigSession,
} from "./ebookConfigApi";

export {
  createEbookAnnotationSession,
  fetchEbookReaderAnnotations,
  createEbookReaderAnnotation,
  updateEbookReaderAnnotation,
  deleteEbookReaderAnnotation,
  type EbookAnnotationSession,
  type EbookReaderAnnotation,
  type EbookReaderAnnotationInput,
} from "./ebookAnnotationsApi";

export function ebookReaderConfigPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/reader-config`;
}
export function ebookReaderAnnotationsPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/annotations`;
}
export function ebookReaderAnnotationPath(contentID: string, annotationID: string): string {
  return `${ebookReaderAnnotationsPath(contentID)}/${encodeURIComponent(annotationID)}`;
}
