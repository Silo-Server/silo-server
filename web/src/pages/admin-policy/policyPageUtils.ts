import { ApiClientError } from "@/api/client";
import type { PolicyCompileIssue } from "@/api/types";

export function formatPolicyDate(value?: string | null) {
  if (!value) return "—";
  return new Date(value).toLocaleString();
}

export function formatPolicyEvalMicros(evalTimeNS?: number | null) {
  if (evalTimeNS === undefined || evalTimeNS === null) return "—";
  return `${Math.round(evalTimeNS / 100) / 10} µs`;
}

export function formatPolicyDomain(domain: string) {
  if (!domain) return "Policy";
  return domain
    .split(/[-_]/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

export function prettyPolicyJson(value: unknown) {
  if (value === undefined || value === null || value === "") return "";
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  return JSON.stringify(value, null, 2);
}

export function defaultPolicySource(domain: string) {
  return `package silo_custom.${domain}

import rego.v1

override(base_decision, input) := base_decision if {
  input.schema_version == 1
}
`;
}

export function toRFC3339FromLocalInput(value: string) {
  if (!value.trim()) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return date.toISOString();
}

function issueFromUnknown(value: unknown): PolicyCompileIssue | null {
  if (typeof value !== "object" || value === null) return null;
  const record = value as Record<string, unknown>;
  if (typeof record.message !== "string") return null;
  return {
    row: typeof record.row === "number" ? record.row : 0,
    col: typeof record.col === "number" ? record.col : 0,
    message: record.message,
  };
}

export function compileIssuesFromError(error: unknown): PolicyCompileIssue[] {
  const body =
    error instanceof ApiClientError
      ? error.body
      : typeof error === "object" && error !== null && "body" in error
        ? (error as { body?: unknown }).body
        : undefined;
  if (typeof body !== "object" || body === null) return [];
  const payload = body as { errors?: unknown };
  if (!Array.isArray(payload.errors)) return [];
  return payload.errors
    .map(issueFromUnknown)
    .filter((issue): issue is PolicyCompileIssue => !!issue);
}

export function messageFromError(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}
