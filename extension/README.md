# OmniHub Chrome Companion Extension

A Manifest V3 extension that lets OmniHub run its built-in linux.do Search in
the Chrome profile you explicitly authorize. It talks to `omnihub chrome-host`
over Chrome Native Messaging, sends only `GET https://linux.do/search.json`,
and does not expose or store the browser's cookies.

## Install

The extension is plain JavaScript. There is no build step, no npm install, and
no dependencies — load the directory as-is.

1. Open `chrome://extensions`.
2. Turn on **Developer mode** (top right).
3. Click **Load unpacked** and select this `extension/` directory.
4. Copy the **ID** shown on the new extension card (32 lowercase letters, e.g.
   `abcdefghijklmnopabcdefghijklmnop`). Chrome derives it from the directory
   path, so it stays stable as long as you keep the directory where it is.
5. Register the native host for that ID:

   ```bash
   omnihub chrome-host install --extension-id <ID>
   ```

   This writes a native messaging manifest naming
   `com.ylxmf2005.omnihub`, pointing at your `omnihub` binary, with
   `allowed_origins` restricted to `chrome-extension://<ID>/`. Chrome will
   refuse to start the host for any other extension.
6. Reload the extension (or restart Chrome) and open the popup. It should say
   **Bridge connected**.

`omnihub chrome-host uninstall` removes the registration again.

## Using it

1. In the OmniHub Dashboard, open the linux.do Channel login link and sign in.
2. Open this extension's popup and click **允许此站点**. Chrome shows its own permission prompt;
   accepting it is the authorization. OmniHub never grants this silently.
3. The Dashboard learns the result from Bridge health — the extension does not
   call the Dashboard API.

The popup lists every allowed site with a **Revoke** button. Revoking calls
`chrome.permissions.remove`; dependent channels then report
`blocked/browser_permission_missing`. Revoking does not touch your login cookies
in the site itself.

**Profile label** is the name the Dashboard shows for this Chrome profile.
Chrome does not expose the real profile name to extensions, so you set it here;
it defaults to `Current Chrome profile`. Saving it reconnects the bridge, because
the host reads the label from the `hello` message.

## What it can and cannot do

The linux.do search handler uses `fetch(..., {credentials: "include"})` inside
Chrome, so Chrome attaches the session itself. Cookie values are not returned
to the Native Host. The extension never writes them to `chrome.storage` or logs
them. It has no `<all_urls>`, `debugger`, content-script, or CDP access.

For every search, the Host and extension independently require the exact
`https://linux.do/*` permission, `/search.json`, a non-empty `q`, and a canonical
positive `page` between 1 and 10, matching Discourse's documented controller bound.
Another host, path, page, redirect, method, or oversized response is refused.

Manifest permissions:

| Permission | Why |
| --- | --- |
| `cookies` | retain the strict legacy `read_cookies` protocol boundary; linux.do Search itself does not export cookie values |
| `nativeMessaging` | connect to `omnihub chrome-host` |
| `storage` | the profile label (`local`) and bridge/backoff state (`session`) |
| `alarms` | reconnect watchdog for the MV3 service worker |
| `optional_host_permissions: ["https://linux.do/*"]` | grants nothing at install; the popup requests only this exact origin after a user gesture |

## Protocol notes

Chrome does the 4-byte little-endian length framing for
`chrome.runtime.connectNative`; this code supplies the JSON objects. Every
message sets `protocol_version: "1.0"` and a `request_id` of 1–128 characters
from `[a-zA-Z0-9._:-]`, and carries only the fields its type allows — the host
decodes with `DisallowUnknownFields` and drops the connection on anything extra.

The extension sends `hello` first, then only `permissions_changed` events and
`result`/`error` replies. `status` requests are answered by the host itself, so
there is no status handler here. Error replies use only
`browser_permission_missing`, `cookie_missing`, `scope_invalid`, and
`browser_request_failed`, with short
fixed messages that never include cookie values or raw Chrome errors.

If the port drops while Chrome is running, the service worker reconnects with
bounded exponential backoff (1s → 60s cap, persisted in `storage.session` so it
survives service worker restarts) plus a 30-second alarm watchdog. It never
tight-loops, and it never sends a keepalive ping, because any unexpected message
type would be a protocol violation.

## Verified and not verified

Statically verified: the manifest is valid MV3 JSON; the JavaScript parses; the
origin-pattern, cookie-domain, and profile-label validators were exercised
against the same cases as `internal/browser/types.go`; and each outgoing message
was captured from the real handlers under a stubbed `chrome` API and checked
field-for-field against the `wireMessage` struct and the host's per-type
field-emptiness rules.

The Go protocol and adapter path are covered by round-trip tests with captured
Native Messaging frames. A real Chrome/linux.do success still depends on the
local installation, the user's login, permission grant, and upstream response.
