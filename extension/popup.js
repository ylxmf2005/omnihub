// OmniHub Chrome Companion popup.
//
// The origin grant must happen here, in the extension's own visible UI, through
// chrome.permissions.request inside a user gesture. The popup never sees cookie
// values: it only reads and changes which origins are allowed.

const stateElement = document.getElementById("bridge-state");
const grantForm = document.getElementById("grant-form");
const hostInput = document.getElementById("host-input");
const grantMessage = document.getElementById("grant-message");
const originList = document.getElementById("origin-list");
const originEmpty = document.getElementById("origin-empty");
const labelForm = document.getElementById("label-form");
const labelInput = document.getElementById("label-input");
const reconnectButton = document.getElementById("reconnect-button");

// Accepts "x.com", "https://x.com", "https://x.com/" or "https://x.com/*" and
// returns the one exact lowercase host, or "" when the input is not a single
// exact HTTPS host. Wildcards, ports, paths and credentials are rejected here
// so the extension never asks Chrome for a broader permission than one origin.
function exactHost(raw) {
  let candidate = String(raw).trim().toLowerCase();
  if (candidate === "") {
    return "";
  }
  candidate = candidate.replace(/^https:\/\//, "");
  candidate = candidate.replace(/\/\*$/, "").replace(/\/$/, "");
  if (candidate === "" || /[*\/:@?#\[\]\s]/.test(candidate)) {
    return "";
  }
  if (candidate.startsWith(".") || candidate.endsWith(".") || candidate.includes("..")) {
    return "";
  }
  if (!/^[a-z0-9.-]+$/.test(candidate) || !candidate.includes(".")) {
    return "";
  }
  return candidate;
}

function patternForHost(host) {
  return `https://${host}/*`;
}

function showMessage(text, isError) {
  grantMessage.textContent = text;
  grantMessage.classList.toggle("error", Boolean(isError));
  grantMessage.hidden = text === "";
}

function renderState(state) {
  if (state.connected) {
    stateElement.textContent = "Bridge connected";
    stateElement.className = "state state-connected";
    return;
  }
  stateElement.textContent = state.last_error
    ? `Bridge offline — ${state.last_error}`
    : "Bridge offline — is omnihub chrome-host installed?";
  stateElement.className = "state state-offline";
}

function renderOrigins(origins) {
  originList.replaceChildren();
  originEmpty.hidden = origins.length > 0;
  for (const origin of origins) {
    const item = document.createElement("li");
    const label = document.createElement("code");
    label.textContent = origin;
    const revoke = document.createElement("button");
    revoke.type = "button";
    revoke.textContent = "Revoke";
    revoke.addEventListener("click", () => {
      void revokeOrigin(origin);
    });
    item.append(label, revoke);
    originList.append(item);
  }
}

async function refresh() {
  let state;
  try {
    state = await chrome.runtime.sendMessage({ type: "omnihub_get_state" });
  } catch {
    state = null;
  }
  if (!state) {
    // The service worker was asleep and has just been woken; read the origins
    // directly so the list is still correct.
    const granted = await chrome.permissions.getAll();
    renderState({ connected: false });
    renderOrigins((granted.origins || []).slice().sort());
    return;
  }
  renderState(state);
  renderOrigins(state.granted_origins || []);
  if (document.activeElement !== labelInput) {
    labelInput.value = state.profile_label || "";
  }
}

async function revokeOrigin(origin) {
  try {
    await chrome.permissions.remove({ origins: [origin] });
    showMessage(`Revoked ${origin}.`, false);
  } catch (error) {
    showMessage(`Chrome refused to revoke ${origin}.`, true);
  }
  await refresh();
}

grantForm.addEventListener("submit", (event) => {
  event.preventDefault();
  const host = exactHost(hostInput.value);
  if (host === "") {
    showMessage("Enter one exact HTTPS host, for example x.com.", true);
    return;
  }
  const pattern = patternForHost(host);
  // Called with no await in front of it so Chrome still sees the user gesture.
  chrome.permissions.request({ origins: [pattern] }, (granted) => {
    if (chrome.runtime.lastError) {
      showMessage("Chrome rejected the permission request.", true);
      return;
    }
    if (!granted) {
      showMessage(`${pattern} was not allowed.`, true);
      return;
    }
    hostInput.value = "";
    showMessage(`Allowed ${pattern}.`, false);
    void refresh();
  });
});

labelForm.addEventListener("submit", (event) => {
  event.preventDefault();
  void (async () => {
    const response = await chrome.runtime.sendMessage({
      type: "omnihub_set_profile_label",
      profile_label: labelInput.value,
    });
    if (response && response.profile_label) {
      labelInput.value = response.profile_label;
    }
    await refresh();
  })();
});

reconnectButton.addEventListener("click", () => {
  void (async () => {
    stateElement.textContent = "Reconnecting…";
    stateElement.className = "state state-unknown";
    await chrome.runtime.sendMessage({ type: "omnihub_reconnect" });
    await refresh();
  })();
});

chrome.permissions.onAdded.addListener(() => {
  void refresh();
});
chrome.permissions.onRemoved.addListener(() => {
  void refresh();
});

void refresh();
