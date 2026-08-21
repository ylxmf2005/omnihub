# OmniHub Chrome Companion Extension

A Manifest V3 extension that lets OmniHub read **only the cookies you explicitly
authorize, for the sites you explicitly allow**, from the Chrome profile you are
using. It talks to `omnihub chrome-host` over Chrome Native Messaging and does
nothing else: no page scripts, no remote endpoints, no cookie storage.

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

1. In the OmniHub Dashboard, open the channel's login link and sign in to the
   site as usual.
2. Open this extension's popup, type the site's exact host (for example
   `x.com`), and click **允许此站点**. Chrome shows its own permission prompt;
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

Cookie values exist only in the local variables of one `read_cookies` handler
call and go only to the native port. They are never written to
`chrome.storage`, never logged, and never sent over the network. The extension
has no `<all_urls>` permission, no `debugger` permission, and does not use the
Chrome DevTools Protocol.

For every request, the extension independently re-checks that the requested
scope names one exact HTTPS host that this profile has actually granted, and
returns only the exact cookie names asked for, only from domains inside the
authorized origin, only from the current cookie store, and only unpartitioned
cookies. Anything outside that is refused rather than widened.

Manifest permissions:

| Permission | Why |
| --- | --- |
| `cookies` | read the authorized cookies |
| `nativeMessaging` | connect to `omnihub chrome-host` |
| `storage` | the profile label (`local`) and bridge/backoff state (`session`) |
| `alarms` | reconnect watchdog for the MV3 service worker |
| `optional_host_permissions: ["https://*/*"]` | **grants nothing at install.** Optional host permissions must be declared in the manifest before they can be requested, and OmniHub cannot know your sites in advance. Chrome only ever grants the one exact `https://<host>/*` you approve in the popup, and that is what `chrome.permissions.getAll()` reports to the host. |

## Protocol notes

Chrome does the 4-byte little-endian length framing for
`chrome.runtime.connectNative`; this code supplies the JSON objects. Every
message sets `protocol_version: "1.0"` and a `request_id` of 1–128 characters
from `[a-zA-Z0-9._:-]`, and carries only the fields its type allows — the host
decodes with `DisallowUnknownFields` and drops the connection on anything extra.

The extension sends `hello` first, then only `permissions_changed` events and
`result`/`error` replies. `status` requests are answered by the host itself, so
there is no status handler here. Error replies use only
`browser_permission_missing`, `cookie_missing`, and `scope_invalid`, with short
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

**Not verified:** the live round trip. Nothing here has been run against a
registered native host, a real Chrome profile, or a real cookie channel, so
`connectNative` startup, the host's hello acceptance, reconnect timing under
real service worker termination, and real `chrome.cookies` results are untested.
