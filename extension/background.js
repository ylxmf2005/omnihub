// OmniHub Chrome Companion — Native Messaging bridge service worker.
//
// Wire contract (must match internal/browser/protocol.go and types.go exactly):
//   - protocol_version is always the string "1.0"
//   - request_id is 1..128 chars from [a-zA-Z0-9._:-]
//   - the host decodes with DisallowUnknownFields, so every message carries
//     ONLY the fields that message type is allowed to carry
//   - after hello the extension sends nothing but permissions_changed events
//     and result/error replies to host requests
//
// Cookie values live in a single handler's local variables for the duration of
// one read_cookies request. They are never logged, stored, or sent anywhere
// other than the native port.

const HOST_NAME = "com.ylxmf2005.omnihub";
const PROTOCOL_VERSION = "1.0";
const WATCHDOG_ALARM = "omnihub-bridge-watchdog";
const BACKOFF_KEY = "bridge_backoff";
const BRIDGE_STATE_KEY = "bridge_state";
const PROFILE_LABEL_KEY = "profile_label";
const DEFAULT_PROFILE_LABEL = "Current Chrome profile";
const MAX_PROFILE_LABEL_BYTES = 128;
const BACKOFF_BASE_MS = 1000;
const BACKOFF_CAP_MS = 60000;
// The host rejects control characters in labels, cookie names and domains.
const CONTROL_CHARACTERS = /[\u0000-\u001F\u007F]/;

// Only these three codes are accepted from the extension; any other code makes
// the host treat the reply as a protocol violation and tear the bridge down.
const ERROR_PERMISSION_MISSING = "browser_permission_missing";
const ERROR_COOKIE_MISSING = "cookie_missing";
const ERROR_SCOPE_INVALID = "scope_invalid";

/** @type {chrome.runtime.Port | null} */
let port = null;
let connecting = null;

// ---------------------------------------------------------------------------
// Origin patterns
// ---------------------------------------------------------------------------

// Mirrors parsePermissionPattern in types.go: exactly one lowercase HTTPS host
// followed by the literal path "/*". Returns the host, or "" when invalid.
function permissionPatternHost(pattern) {
  if (typeof pattern !== "string" || !pattern.startsWith("https://") || !pattern.endsWith("/*")) {
    return "";
  }
  const host = pattern.slice("https://".length, pattern.length - "/*".length);
  if (host === "" || host !== host.toLowerCase() || host.trim() !== host) {
    return "";
  }
  if (/[*\/:@?#\[\]]/.test(host) || host.startsWith(".") || host.includes("..")) {
    return "";
  }
  return host;
}

function patternForHost(host) {
  return `https://${host}/*`;
}

// Mirrors normalizeDomain in types.go: lowercase, trimmed, one leading dot off.
function normalizeDomain(domain) {
  const trimmed = String(domain).trim().toLowerCase();
  return trimmed.startsWith(".") ? trimmed.slice(1) : trimmed;
}

// Mirrors validCookieDomain in types.go.
function validCookieDomain(domain) {
  return (
    typeof domain === "string" &&
    domain === domain.trim() &&
    domain === domain.toLowerCase() &&
    domain !== "" &&
    !domain.startsWith("..") &&
    !/[\/:@?#]/.test(domain) &&
    !CONTROL_CHARACTERS.test(domain)
  );
}

// The full set of origins Chrome currently grants us, normalized to the exact
// shape the host accepts. Anything that does not parse is dropped rather than
// sent, because one invalid entry makes the host reject the whole message.
async function grantedOrigins() {
  const granted = await chrome.permissions.getAll();
  const unique = new Set();
  for (const origin of granted.origins || []) {
    if (permissionPatternHost(origin) !== "") {
      unique.add(origin);
    }
  }
  return Array.from(unique).sort();
}

// ---------------------------------------------------------------------------
// Request ids
// ---------------------------------------------------------------------------

function newRequestId(kind) {
  const random = new Uint8Array(16);
  crypto.getRandomValues(random);
  const hex = Array.from(random, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `omnihub_${kind}_${hex}`;
}

// ---------------------------------------------------------------------------
// Profile label
// ---------------------------------------------------------------------------

// The host requires a trimmed, non-empty, control-free label of at most 128
// bytes. Chrome gives extensions no access to the profile name, so the label is
// user-editable in the popup and defaults to a constant.
function sanitizeProfileLabel(candidate) {
  if (typeof candidate !== "string") {
    return DEFAULT_PROFILE_LABEL;
  }
  const label = candidate.replace(/[\u0000-\u001F\u007F]/g, " ").trim();
  if (label === "") {
    return DEFAULT_PROFILE_LABEL;
  }
  const encoder = new TextEncoder();
  if (encoder.encode(label).length <= MAX_PROFILE_LABEL_BYTES) {
    return label;
  }
  let truncated = label;
  while (truncated !== "" && encoder.encode(truncated).length > MAX_PROFILE_LABEL_BYTES) {
    truncated = truncated.slice(0, -1);
  }
  const trimmed = truncated.trim();
  return trimmed === "" ? DEFAULT_PROFILE_LABEL : trimmed;
}

async function profileLabel() {
  const stored = await chrome.storage.local.get(PROFILE_LABEL_KEY);
  return sanitizeProfileLabel(stored[PROFILE_LABEL_KEY]);
}

// ---------------------------------------------------------------------------
// Bridge state surfaced to the popup (never contains cookie data)
// ---------------------------------------------------------------------------

async function setBridgeState(patch) {
  const current = (await chrome.storage.session.get(BRIDGE_STATE_KEY))[BRIDGE_STATE_KEY] || {};
  const next = { ...current, ...patch, updated_at: new Date().toISOString() };
  await chrome.storage.session.set({ [BRIDGE_STATE_KEY]: next });
}

// ---------------------------------------------------------------------------
// Native port lifecycle
// ---------------------------------------------------------------------------

function post(message) {
  if (!port) {
    return false;
  }
  try {
    port.postMessage(message);
    return true;
  } catch (error) {
    console.warn("omnihub: native post failed", error && error.message);
    return false;
  }
}

async function readBackoff() {
  const stored = await chrome.storage.session.get(BACKOFF_KEY);
  return stored[BACKOFF_KEY] || { failures: 0, next_attempt_at: 0 };
}

async function resetBackoff() {
  await chrome.storage.session.set({ [BACKOFF_KEY]: { failures: 0, next_attempt_at: 0 } });
}

// Bounded exponential backoff, persisted in session storage so it survives
// service worker restarts and never becomes a reconnect tight loop.
async function recordFailure() {
  const previous = await readBackoff();
  const failures = Math.min(previous.failures + 1, 16);
  const delay = Math.min(BACKOFF_BASE_MS * 2 ** (failures - 1), BACKOFF_CAP_MS);
  await chrome.storage.session.set({
    [BACKOFF_KEY]: { failures, next_attempt_at: Date.now() + delay },
  });
  setTimeout(() => {
    void ensureConnected();
  }, delay);
}

// Connects if there is no live port and the backoff window has elapsed.
// Resolves true when this call established a new connection.
async function ensureConnected() {
  if (port) {
    return false;
  }
  if (connecting) {
    return connecting;
  }
  const backoff = await readBackoff();
  if (backoff.next_attempt_at > Date.now()) {
    return false;
  }
  connecting = connect().finally(() => {
    connecting = null;
  });
  return connecting;
}

async function connect() {
  let opened;
  try {
    opened = chrome.runtime.connectNative(HOST_NAME);
  } catch (error) {
    console.warn("omnihub: connectNative failed", error && error.message);
    await setBridgeState({ connected: false, last_error: "connect_failed" });
    await recordFailure();
    return false;
  }

  port = opened;
  opened.onMessage.addListener((message) => {
    void handleHostMessage(message);
  });
  opened.onDisconnect.addListener(() => {
    void handleDisconnect(opened);
  });

  // The host blocks until it receives a valid hello and rejects anything else.
  const hello = {
    protocol_version: PROTOCOL_VERSION,
    type: "hello",
    request_id: newRequestId("hello"),
    profile_label: await profileLabel(),
    granted_origins: await grantedOrigins(),
  };
  if (!post(hello)) {
    return false;
  }
  await resetBackoff();
  await setBridgeState({ connected: true, last_error: null, profile_label: hello.profile_label });
  return true;
}

async function handleDisconnect(disconnected) {
  // Chrome's own message ("Specified native messaging host not found.", "Native
  // host has exited.", ...). It never contains browser data.
  const reason = (chrome.runtime.lastError && chrome.runtime.lastError.message) || null;
  if (port === disconnected) {
    port = null;
  }
  console.info("omnihub: native bridge disconnected", reason || "");
  await setBridgeState({ connected: false, last_error: reason });
  await recordFailure();
}

// ---------------------------------------------------------------------------
// Host → extension messages
// ---------------------------------------------------------------------------

function replyError(requestId, code, message) {
  post({
    protocol_version: PROTOCOL_VERSION,
    type: "error",
    request_id: requestId,
    error: { code, message },
  });
}

async function handleHostMessage(message) {
  if (!message || typeof message !== "object" || message.protocol_version !== PROTOCOL_VERSION) {
    console.warn("omnihub: ignoring native message with an unexpected protocol version");
    return;
  }
  switch (message.type) {
    case "read_cookies":
      await handleReadCookies(message);
      return;
    case "revoke_permission":
      await handleRevokePermission(message);
      return;
    case "result":
      // Reply to our own hello or permissions_changed; nothing to do.
      return;
    case "error":
      // The host rejected one of our messages. Log the code only.
      console.warn("omnihub: host rejected a message", message.error && message.error.code);
      return;
    default:
      // Never answer an unknown type: an unexpected result/error would be a
      // protocol violation on the host side and would kill the bridge.
      console.warn("omnihub: ignoring unsupported native message type");
  }
}

// Re-checks locally that the requested scope is one exact HTTPS host, the
// current store and unpartitioned cookies only. The host validates this too;
// the extension is the component that actually touches cookies, so it does not
// take the request on trust.
function validateCookieScope(message) {
  const host = permissionPatternHost(message.permission_origin_pattern);
  if (host === "") {
    return { error: "permission origin pattern is invalid" };
  }
  const scope = message.cookie_scope;
  if (!scope || typeof scope !== "object") {
    return { error: "cookie scope is required" };
  }
  if (typeof message.channel_id !== "string" || message.channel_id.trim() === "") {
    return { error: "channel id is required" };
  }
  let url;
  try {
    url = new URL(scope.url);
  } catch {
    return { error: "cookie scope URL is invalid" };
  }
  if (
    url.protocol !== "https:" ||
    url.hostname === "" ||
    url.port !== "" ||
    url.username !== "" ||
    url.password !== "" ||
    url.search !== "" ||
    url.hash !== "" ||
    normalizeDomain(url.hostname) !== host
  ) {
    return { error: "cookie scope URL is outside the permission origin" };
  }
  if (!Array.isArray(scope.names) || scope.names.length === 0) {
    return { error: "cookie names are required" };
  }
  const names = [];
  for (const name of scope.names) {
    if (typeof name !== "string" || name === "" || name !== name.trim() || CONTROL_CHARACTERS.test(name)) {
      return { error: "cookie names must be explicit" };
    }
    if (names.includes(name)) {
      return { error: "cookie names must be unique" };
    }
    names.push(name);
  }
  if (!Array.isArray(scope.allowed_domains) || scope.allowed_domains.length === 0) {
    return { error: "cookie domains are required" };
  }
  const allowedDomains = new Set();
  for (const domain of scope.allowed_domains) {
    if (!validCookieDomain(domain) || normalizeDomain(domain) !== host) {
      return { error: "allowed cookie domain is outside the permission origin" };
    }
    allowedDomains.add(normalizeDomain(domain));
  }
  if (scope.store !== "current") {
    return { error: "only the current Chrome cookie store is supported" };
  }
  if (!Array.isArray(scope.partitions) || scope.partitions.length !== 1 || scope.partitions[0] !== "unpartitioned") {
    return { error: "only unpartitioned cookies are supported" };
  }
  return { host, url: scope.url, names, allowedDomains, store: scope.store, partition: scope.partitions[0] };
}

// Chrome reports unpartitioned cookies without a partition key; a partition key
// whose top-level site is empty is treated the same way.
function isUnpartitioned(cookie) {
  return !cookie.partitionKey || !cookie.partitionKey.topLevelSite;
}

async function handleReadCookies(message) {
  const requestId = message.request_id;
  const scope = validateCookieScope(message);
  if (scope.error) {
    replyError(requestId, ERROR_SCOPE_INVALID, scope.error);
    return;
  }

  const pattern = patternForHost(scope.host);
  let permitted = false;
  try {
    permitted = await chrome.permissions.contains({ origins: [pattern] });
  } catch {
    permitted = false;
  }
  if (!permitted) {
    replyError(requestId, ERROR_PERMISSION_MISSING, "this Chrome profile has not granted the origin");
    return;
  }

  const cookies = [];
  for (const name of scope.names) {
    let candidates;
    try {
      // One query per authorized name: Chrome only ever hands back cookies whose
      // names the request explicitly listed. storeId is omitted, so this reads
      // the current (regular profile) store; partitionKey is omitted, so only
      // unpartitioned cookies are returned.
      candidates = await chrome.cookies.getAll({ url: scope.url, name });
    } catch {
      replyError(requestId, ERROR_COOKIE_MISSING, "Chrome did not return the authorized cookies");
      return;
    }
    // getAll orders longest path first, so the first in-scope match is the most
    // specific cookie Chrome would send to the scope URL.
    const match = candidates.find(
      (cookie) =>
        cookie.name === name &&
        typeof cookie.value === "string" &&
        cookie.value !== "" &&
        isUnpartitioned(cookie) &&
        typeof cookie.path === "string" &&
        cookie.path.startsWith("/") &&
        validCookieDomain(cookie.domain) &&
        scope.allowedDomains.has(normalizeDomain(cookie.domain)),
    );
    if (!match) {
      replyError(requestId, ERROR_COOKIE_MISSING, "an authorized cookie is absent or empty");
      return;
    }
    cookies.push({
      name: match.name,
      value: match.value,
      domain: match.domain,
      path: match.path,
      // Echo the scope's own store and partition labels: the host compares these
      // against the request, not against Chrome's internal store id.
      store: scope.store,
      partition: scope.partition,
    });
  }

  post({
    protocol_version: PROTOCOL_VERSION,
    type: "result",
    request_id: requestId,
    cookies,
  });
}

async function handleRevokePermission(message) {
  const requestId = message.request_id;
  const host = permissionPatternHost(message.permission_origin_pattern);
  if (host === "") {
    replyError(requestId, ERROR_SCOPE_INVALID, "permission origin pattern is invalid");
    return;
  }
  const pattern = patternForHost(host);
  try {
    await chrome.permissions.remove({ origins: [pattern] });
  } catch (error) {
    console.warn("omnihub: permissions.remove failed", error && error.message);
  }
  const origins = await grantedOrigins();
  if (origins.includes(pattern)) {
    replyError(requestId, ERROR_PERMISSION_MISSING, "Chrome did not remove the origin permission");
    return;
  }
  post({
    protocol_version: PROTOCOL_VERSION,
    type: "result",
    request_id: requestId,
    granted_origins: origins,
  });
}

// ---------------------------------------------------------------------------
// Permission changes made through the extension's own UI
// ---------------------------------------------------------------------------

async function announcePermissions() {
  // A fresh connection carries the current list in its hello, so no event is
  // needed on top of it.
  const connected = await ensureConnected();
  if (connected || !port) {
    return;
  }
  post({
    protocol_version: PROTOCOL_VERSION,
    type: "permissions_changed",
    request_id: newRequestId("perm"),
    granted_origins: await grantedOrigins(),
  });
}

chrome.permissions.onAdded.addListener(() => {
  void announcePermissions();
});
chrome.permissions.onRemoved.addListener(() => {
  void announcePermissions();
});

// ---------------------------------------------------------------------------
// Popup messaging
// ---------------------------------------------------------------------------

chrome.runtime.onMessage.addListener((request, _sender, sendResponse) => {
  if (!request || typeof request !== "object") {
    return false;
  }
  if (request.type === "omnihub_get_state") {
    void (async () => {
      const state = (await chrome.storage.session.get(BRIDGE_STATE_KEY))[BRIDGE_STATE_KEY] || {};
      sendResponse({
        connected: Boolean(port) && state.connected === true,
        last_error: state.last_error || null,
        profile_label: await profileLabel(),
        granted_origins: await grantedOrigins(),
      });
    })();
    return true;
  }
  if (request.type === "omnihub_reconnect") {
    void (async () => {
      await resetBackoff();
      const connected = await ensureConnected();
      sendResponse({ connected: connected || Boolean(port) });
    })();
    return true;
  }
  if (request.type === "omnihub_set_profile_label") {
    void (async () => {
      const label = sanitizeProfileLabel(request.profile_label);
      await chrome.storage.local.set({ [PROFILE_LABEL_KEY]: label });
      // The host learns the label in hello only, so reconnect to publish it.
      // Chrome does not fire onDisconnect for the side that disconnects, so the
      // port is cleared here; the pause lets the old host release the endpoint
      // before the replacement tries to claim it.
      if (port) {
        port.disconnect();
        port = null;
        await new Promise((resolve) => setTimeout(resolve, 300));
      }
      await resetBackoff();
      await ensureConnected();
      sendResponse({ profile_label: label });
    })();
    return true;
  }
  return false;
});

// ---------------------------------------------------------------------------
// Startup and watchdog
// ---------------------------------------------------------------------------

// The watchdog both reconnects after a dropped port and wakes the service worker
// often enough that the bridge does not stay down while Chrome is running.
async function startWatchdog() {
  await chrome.alarms.create(WATCHDOG_ALARM, { periodInMinutes: 0.5 });
}

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === WATCHDOG_ALARM) {
    void ensureConnected();
  }
});

chrome.runtime.onInstalled.addListener(() => {
  void startWatchdog();
  void ensureConnected();
});

chrome.runtime.onStartup.addListener(() => {
  void startWatchdog();
  void ensureConnected();
});

// Every service worker start (including a wake after termination) reconnects,
// still subject to the persisted backoff window.
void startWatchdog();
void ensureConnected();
