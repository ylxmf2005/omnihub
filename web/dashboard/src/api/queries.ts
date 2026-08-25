/**
 * Data layer.
 *
 * Every page reads and writes through this file so that three things are done
 * once and identically everywhere:
 *
 *   1. **ETag round-tripping.** Detail queries keep the strong ETag next to the
 *      body; `PUT`/`DELETE`/revoke replay it verbatim as `If-Match`. A page
 *      cannot accidentally write without one, because the mutation signature
 *      demands it.
 *   2. **Revision conflicts stay loud.** A 409 is surfaced to the caller as a
 *      `ProblemError` with `isRevisionConflict`; nothing here retries or
 *      re-reads-then-overwrites. The user decides.
 *   3. **Credential values never return to the browser.** They can be replaced
 *      or revoked, but Dashboard reads expose only masked metadata.
 *
 * Demo mode (`VITE_OMNIHUB_LIVE !== '1'`) serves the six endpoints that have
 * captured fixtures and returns empty collections for the rest, so the app
 * renders honest empty states instead of crashing without a Backend. Live mode
 * is the product path.
 */

import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryKey,
  type UseQueryOptions,
} from '@tanstack/react-query'
import {
  del,
  get,
  newIdempotencyKey,
  post,
  postWithMatch,
  put,
  type Fetched,
} from './client'
import type {
  AuthorizationDescriptor,
  BrowserBridge,
  Channel,
  Credential,
  EgressProfile,
  EndpointProfile,
  Envelope,
  OperationKind,
  QueryInputFor,
  ReadinessReport,
  Run,
  RunStatus,
  Source,
  RouteTemplate,
  ViewDetailResponse,
  ViewItems,
  ViewListEntry,
} from './types'
import { isTerminalRun } from './types'

import readinessFixture from '../fixtures/readiness.json'
import viewsFixture from '../fixtures/views.json'
import runsFixture from '../fixtures/runs.json'
import channelsFixture from '../fixtures/channels.json'
import bridgesFixture from '../fixtures/browser-bridges.json'
import egressFixture from '../fixtures/egress-profiles.json'
import sourcesFixture from '../fixtures/sources.json'
import routeTemplatesFixture from '../fixtures/route-templates.json'

/**
 * Demo mode is opt-in, and the default is live.
 *
 * The polarity matters: this bundle is embedded into `omnihub serve` and served
 * from the same origin as the API, so the shipped product must talk to the real
 * Backend. An earlier version defaulted to fixtures unless `VITE_OMNIHUB_LIVE=1`
 * was set, which meant the production build silently rendered a read-only
 * snapshot with every write button disabled. Demo mode now has to be asked for
 * explicitly with `VITE_OMNIHUB_DEMO=1`.
 */
export const USE_FIXTURES = import.meta.env.VITE_OMNIHUB_DEMO === '1'

/** True when a write cannot possibly succeed, so pages can disable their forms. */
export const READ_ONLY = USE_FIXTURES

function resolved<T>(value: unknown): Promise<T> {
  return Promise.resolve(value as T)
}

// ---------------------------------------------------------------------------
// Query keys. One place, so invalidation after a write cannot miss a list.
// ---------------------------------------------------------------------------

export const keys = {
  readiness: ['readiness'] as QueryKey,
  channels: ['channels'] as QueryKey,
  channel: (id: string) => ['channels', id] as QueryKey,
  views: ['views'] as QueryKey,
  view: (id: string) => ['views', id] as QueryKey,
  viewItems: (id: string) => ['views', id, 'items'] as QueryKey,
  runs: (limit: number) => ['runs', limit] as QueryKey,
  run: (id: string) => ['runs', id] as QueryKey,
  credentials: ['credentials'] as QueryKey,
  egress: ['egress-profiles'] as QueryKey,
  endpoints: ['endpoint-profiles'] as QueryKey,
  sources: ['sources'] as QueryKey,
  routeTemplates: ['route-templates'] as QueryKey,
  bridge: ['browser-bridges'] as QueryKey,
  chromeDescriptor: (id: string) => ['channels', id, 'chrome'] as QueryKey,
}

/** Lists that any write might invalidate: readiness and the summary are derived. */
const DERIVED_KEYS: QueryKey[] = [keys.readiness]

// ---------------------------------------------------------------------------
// Read primitives
// ---------------------------------------------------------------------------

/** A list read. Returns the body only; lists are not written to directly. */
function useList<T>(
  key: QueryKey,
  path: string,
  fixture: unknown,
  options?: Partial<UseQueryOptions<T>>,
) {
  return useQuery<T>({
    queryKey: key,
    queryFn: () =>
      USE_FIXTURES ? resolved<T>(fixture) : get<T>(path).then((response) => response.data),
    ...options,
  })
}

/**
 * A detail read that keeps its ETag. The whole `Fetched<T>` is the query data on
 * purpose: a page that wants to edit must hold the ETag it read, and separating
 * them invites writing with a stale one.
 */
export function useDetail<T>(key: QueryKey, path: string, enabled = true) {
  return useQuery<Fetched<T>>({
    queryKey: key,
    queryFn: () => get<T>(path),
    enabled: enabled && !USE_FIXTURES,
  })
}

// ---------------------------------------------------------------------------
// Dashboard, readiness
// ---------------------------------------------------------------------------

export function useReadiness() {
  return useList<ReadinessReport>(keys.readiness, '/v1/readiness', readinessFixture)
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

export function useChannels() {
  return useList<Channel[]>(keys.channels, '/v1/channels', channelsFixture)
}

export function useChannel(id: string | undefined) {
  return useDetail<Channel>(keys.channel(id ?? ''), `/v1/channels/${id}`, Boolean(id))
}

export function useChromeDescriptor(channelId: string | undefined) {
  return useDetail<AuthorizationDescriptor>(
    keys.chromeDescriptor(channelId ?? ''),
    `/v1/channels/${channelId}/chrome/authorization-descriptor`,
    Boolean(channelId),
  )
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

export function useViews() {
  return useList<ViewListEntry[]>(keys.views, '/v1/views', viewsFixture)
}

/**
 * A single View. The route returns the same wrapper as a list entry — `{view,
 * status, snapshot, feed_urls}` — so the resource itself is at `.data.view`.
 */
export function useView(id: string | undefined) {
  return useDetail<ViewDetailResponse>(keys.view(id ?? ''), `/v1/views/${id}`, Boolean(id))
}

/**
 * GET /v1/views/{id}/items. The `limit` parameter is accepted but ignored — the
 * Backend always returns the whole snapshot — so callers must not present a
 * partial list as if it were bounded.
 */
export function useViewItems(id: string | undefined) {
  return useQuery<ViewItems>({
    queryKey: keys.viewItems(id ?? ''),
    queryFn: () => get<ViewItems>(`/v1/views/${id}/items`).then((response) => response.data),
    enabled: Boolean(id) && !USE_FIXTURES,
  })
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export function useRuns(limit = 20) {
  return useList<Run[]>(keys.runs(limit), `/v1/runs?limit=${limit}`, runsFixture)
}

/**
 * A single Run, polled until it reaches a terminal status and then left alone.
 * The Backend has no push channel by design, so this is the only way a page
 * learns that a 202 finished.
 */
export function useRun(id: string | undefined, poll = true) {
  return useQuery<Run>({
    queryKey: keys.run(id ?? ''),
    queryFn: () => get<Run>(`/v1/runs/${id}`).then((response) => response.data),
    enabled: Boolean(id) && !USE_FIXTURES,
    refetchInterval: (query) => {
      if (!poll) return false
      const status = query.state.data?.status as RunStatus | undefined
      if (!status) return 1000
      return isTerminalRun(status) ? false : 1000
    },
  })
}

// ---------------------------------------------------------------------------
// Registry resources
// ---------------------------------------------------------------------------

export function useEgressProfiles() {
  return useList<EgressProfile[]>(keys.egress, '/v1/egress-profiles', egressFixture)
}

export function useEndpointProfiles() {
  return useList<EndpointProfile[]>(keys.endpoints, '/v1/endpoint-profiles', [])
}

export function useCredentials() {
  return useList<Credential[]>(keys.credentials, '/v1/credentials', [])
}

export function useSources() {
  return useList<Source[]>(keys.sources, '/v1/sources', sourcesFixture)
}

export function useRouteTemplates() {
  return useList<RouteTemplate[]>(keys.routeTemplates, '/v1/route-templates', routeTemplatesFixture)
}

export function useBridge() {
  return useList<BrowserBridge>(keys.bridge, '/v1/browser-bridges', bridgesFixture)
}

// ---------------------------------------------------------------------------
// Write primitives
// ---------------------------------------------------------------------------

/** Lists to refresh after a write, always including the derived reads. */
function invalidate(client: ReturnType<typeof useQueryClient>, own: QueryKey[]) {
  for (const key of [...own, ...DERIVED_KEYS]) {
    void client.invalidateQueries({ queryKey: key })
  }
}

/** POST to a collection. No ETag: there is nothing yet to conflict with. */
export function useCreate<TInput, TOut>(path: string, affected: QueryKey[]) {
  const client = useQueryClient()
  return useMutation<TOut, Error, TInput>({
    mutationFn: (body) => post<TOut>(path, body).then((response) => response.data),
    onSuccess: () => invalidate(client, affected),
  })
}

/**
 * PUT with a mandatory ETag. A 409 propagates untouched — callers must show the
 * conflict and let the user re-read, never merge silently.
 */
export function useUpdate<TInput, TOut>(pathOf: (id: string) => string, affected: QueryKey[]) {
  const client = useQueryClient()
  return useMutation<TOut, Error, { id: string; etag: string; body: TInput }>({
    mutationFn: ({ id, etag, body }) =>
      put<TOut>(pathOf(id), body, etag).then((response) => response.data),
    onSuccess: () => invalidate(client, affected),
  })
}

/** DELETE with a mandatory ETag. `resource_in_use` surfaces as-is; no cascade. */
export function useDelete(pathOf: (id: string) => string, affected: QueryKey[]) {
  const client = useQueryClient()
  return useMutation<void, Error, { id: string; etag: string }>({
    mutationFn: ({ id, etag }) => del(pathOf(id), etag),
    onSuccess: () => invalidate(client, affected),
  })
}

// ---------------------------------------------------------------------------
// Actions that create Runs
// ---------------------------------------------------------------------------

/**
 * POST /v1/channels/{id}/probe. Returns the 202 Run so the caller can poll it.
 * The idempotency key is minted per attempt, so a double-click cannot enqueue
 * two probes.
 */
export function useProbeChannel() {
  const client = useQueryClient()
  return useMutation<Run, Error, string>({
    mutationFn: (channelId) =>
      post<Run>(`/v1/channels/${channelId}/probe`, undefined, {
        idempotencyKey: newIdempotencyKey(`probe-${channelId}`),
      }).then((response) => response.data),
    onSuccess: () => invalidate(client, [keys.channels, keys.runs(20)]),
  })
}

/** POST /v1/views/{id}/refresh. Also a 202 Run. */
export function useRefreshView() {
  const client = useQueryClient()
  return useMutation<Run, Error, string>({
    mutationFn: (viewId) =>
      post<Run>(`/v1/views/${viewId}/refresh`, undefined, {
        idempotencyKey: newIdempotencyKey(`refresh-${viewId}`),
      }).then((response) => response.data),
    onSuccess: (_run, viewId) =>
      invalidate(client, [keys.views, keys.view(viewId), keys.viewItems(viewId), keys.runs(20)]),
  })
}

/** POST /v1/credentials/{id}/revoke. Requires the ETag; revocation is a write. */
export function useRevokeCredential() {
  const client = useQueryClient()
  return useMutation<Credential, Error, { id: string; etag: string }>({
    mutationFn: ({ id, etag }) =>
      postWithMatch<Credential>(`/v1/credentials/${id}/revoke`, etag).then(
        (response) => response.data,
      ),
    onSuccess: () => invalidate(client, [keys.credentials]),
  })
}

// ---------------------------------------------------------------------------
// Query plane
// ---------------------------------------------------------------------------

/**
 * POST /v1/search|latest|fetch — synchronous execution, returns an Envelope.
 *
 * Typed per kind because the three endpoints accept three different bodies (see
 * `QueryInputFor`): `fetch` takes `target` and rejects `limit`, `search` requires
 * `query`. All three set `additionalProperties: false`, so a superset body fails.
 *
 * Not cached: a query is an action with a cost, and re-running it must be the
 * user's decision. A `partial` result is a success with gaps, so it resolves
 * rather than throwing; only a transport or pre-execution failure rejects.
 *
 * These calls create no Run — the Query plane executes synchronously and persists
 * nothing, so `/v1/runs` never contains a `query` kind. Nothing is invalidated
 * here for that reason.
 */
export function useRunQuery<K extends OperationKind>(kind: K) {
  return useMutation<Envelope, Error, QueryInputFor<K>>({
    mutationFn: (body) => post<Envelope>(`/v1/${kind}`, body).then((response) => response.data),
  })
}
