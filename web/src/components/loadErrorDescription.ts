import { V2TimeoutError } from "@/api/v2/request";

/**
 * The explanation under a "Couldn't load …" heading. A read that ran out its
 * deadline says the server is not answering, which tells the viewer the
 * problem is not the page they asked for.
 */
export function loadErrorDescription(error: unknown): string {
  return error instanceof V2TimeoutError
    ? "The server isn't responding. Try again in a moment."
    : "Something went wrong while loading it. Try again in a moment.";
}
