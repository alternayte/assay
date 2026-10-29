import createClient from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export const api = createClient<paths>({ baseUrl: "" });

export type Schemas = components["schemas"];

/** An error from the API, with its stable code. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly retryAfter?: number,
  ) {
    super(message);
  }
}

type Envelope = { error?: { code?: string; message?: string } };

/** Returns the data of a response, or throws an ApiError. */
export function unwrap<T>(r: { data?: T; error?: unknown; response: Response }): T {
  if (r.error !== undefined || !r.response.ok) {
    const e = (r.error ?? {}) as Envelope;
    const retry = Number(r.response.headers.get("Retry-After") ?? "");
    throw new ApiError(
      r.response.status,
      (e.error?.code ?? "error").toLowerCase(),
      e.error?.message ?? r.response.statusText,
      Number.isFinite(retry) && retry > 0 ? retry : undefined,
    );
  }
  return r.data as T;
}

export function errorCode(err: unknown): string | undefined {
  return err instanceof ApiError ? err.code : undefined;
}
