/**
 * Thin API client.
 *
 * Responsibilities kept here so pages never re-implement them:
 *  - strong ETag capture, and `If-Match` on PUT / DELETE / credential revoke
 *  - both error shapes: RFC 9457 `application/problem+json` for pre-execution
 *    failures, and a readable Envelope body that can accompany a 5xx when every
 *    route failed. A 502 with an explicable Envelope is not a bare toast.
 *  - `Idempotency-Key` on every Run-creating call
 */

import type { Problem } from './types'

/** A pre-execution failure carrying a Problem document. */
export class ProblemError extends Error {
  readonly problem: Problem
  readonly status: number

  constructor(problem: Problem) {
    super(problem.detail ?? problem.title)
    this.name = 'ProblemError'
    this.problem = problem
    this.status = problem.status
  }

  /** 409 from a stale `If-Match`. The caller must re-read and let the user decide. */
  get isRevisionConflict(): boolean {
    return this.problem.code === 'revision_conflict'
  }

  /** 409 from deleting a referenced resource. The UI shows dependents, never cascades. */
  get isResourceInUse(): boolean {
    return this.problem.code === 'resource_in_use'
  }
}

/**
 * A failed execution whose body is still a full Envelope. The result is
 * meaningful — coverage, errors and provenance are all present — so it is
 * surfaced as data rather than as a generic request failure.
 */
export class EnvelopeError extends Error {
  readonly envelope: unknown
  readonly status: number

  constructor(envelope: unknown, status: number) {
    super('execution failed')
    this.name = 'EnvelopeError'
    this.envelope = envelope
    this.status = status
  }
}

export interface Fetched<T> {
  data: T
  /** Strong ETag, replayed verbatim into `If-Match`. Never re-derived. */
  etag: string | null
}

function isProblem(value: unknown): value is Problem {
  return (
    typeof value === 'object' &&
    value !== null &&
    'code' in value &&
    'status' in value &&
    'title' in value
  )
}

async function parse<T>(response: Response): Promise<Fetched<T>> {
  const contentType = response.headers.get('content-type') ?? ''
  const body = contentType.includes('json') ? await response.json() : null

  if (!response.ok) {
    if (contentType.includes('application/problem+json') && isProblem(body)) {
      throw new ProblemError(body)
    }
    // An Envelope body on a failing status: keep it, it explains itself.
    if (body !== null && typeof body === 'object' && 'status' in body && 'coverage' in body) {
      throw new EnvelopeError(body, response.status)
    }
    throw new Error(`HTTP ${response.status} ${response.statusText}`)
  }

  return { data: body as T, etag: response.headers.get('etag') }
}

export async function get<T>(path: string): Promise<Fetched<T>> {
  return parse<T>(await fetch(path, { headers: { Accept: 'application/json' } }))
}

export async function post<T>(
  path: string,
  body?: unknown,
  options: { idempotencyKey?: string } = {},
): Promise<Fetched<T>> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey

  return parse<T>(
    await fetch(path, {
      method: 'POST',
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  )
}

/**
 * A POST that mutates existing state and therefore carries `If-Match` — today
 * only credential revocation. Kept next to `put`/`del` so revocation shares
 * their conflict handling instead of hand-rolling a fetch that would swallow a
 * `409 revision_conflict`.
 */
export async function postWithMatch<T>(
  path: string,
  etag: string,
  body?: unknown,
): Promise<Fetched<T>> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'If-Match': etag,
  }
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  return parse<T>(
    await fetch(path, {
      method: 'POST',
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  )
}

/** Full replacement, not PATCH. The ETag is mandatory. */
export async function put<T>(path: string, body: unknown, etag: string): Promise<Fetched<T>> {
  return parse<T>(
    await fetch(path, {
      method: 'PUT',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        'If-Match': etag,
      },
      body: JSON.stringify(body),
    }),
  )
}

export async function del(path: string, etag: string): Promise<void> {
  await parse<void>(
    await fetch(path, {
      method: 'DELETE',
      headers: { Accept: 'application/json', 'If-Match': etag },
    }),
  )
}

/**
 * Stable per-attempt key so a double-click cannot create two Runs. Callers hold
 * one key for the lifetime of an attempt and only mint a new one on retry.
 */
export function newIdempotencyKey(prefix: string): string {
  return `${prefix}-${crypto.randomUUID()}`
}
