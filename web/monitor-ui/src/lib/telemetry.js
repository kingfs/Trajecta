// The explicit extensions are what let the unit tests import this module in
// node, which does not resolve extensionless specifiers - the same reason
// lib/realtime.js spells them out.
import { apiPaths, requestJSON } from "./api.js";
import { queryClient } from "./queryClient.ts";

/*
 * The anonymous install ping.
 *
 * What this is for: answering "how many installations run Trajecta, and which
 * version each of them is on". What it deliberately is not: analytics on people.
 *
 * The properties that matter, and the reason each one is written down here:
 *
 *   - It is off by default. The switch lives in the server's configuration
 *     (`telemetry.enabled` plus a `telemetry.endpoint`); the page reads it from
 *     /api/about and does nothing when it is off. A local-first proxy records
 *     whatever its users point at it, so phoning home may not be something a
 *     configuration file forgot to mention.
 *
 *   - The identity is the deployment's, not the browser's. It comes from
 *     /api/about as `install_id`, a random value the server keeps in its own
 *     database, so the five people who share one Monitor are one installation
 *     rather than five. Nothing is derived from the machine, the operator or
 *     the recordings. When the server cannot supply one - its database is
 *     unavailable - nothing is sent at all. A browser-scoped fallback would
 *     keep the request count up while turning "installations" into "browsers",
 *     which is the one number this feature exists to report.
 *
 *   - It carries the version, the commit, the platform and the install id, and
 *     nothing else. Never a URL, a model, a trace id, a token or a username.
 *
 *   - It is silent. At most one request per browser per `interval` (a day by
 *     default), and one immediately after a version change, because which
 *     version an installation moved to is the interesting half of the data. No
 *     failures are surfaced, retried or reported: the only trace of a failure is
 *     that the count is lower than it would have been.
 */

// Only the throttle lives in the browser. The identity is the server's.
export const INSTALL_PING_KEY = "trajecta.monitor.installPing";

// Fired on window after a report is handed to the browser, so the About page can
// show when this browser last reported without polling storage.
export const INSTALL_PING_EVENT = "trajecta:install-ping";

// The query key useJSON builds for a dependency-free read, spelled out so the
// ping and the About page share one request instead of making two.
export const ABOUT_QUERY_KEY = ["GET", apiPaths.about];

function readStoredJSON(storage, key) {
  try {
    const raw = storage.getItem(key);
    return raw ? JSON.parse(raw) : null;
  } catch {
    // A storage that is full, disabled or holds something another build wrote.
    return null;
  }
}

function writeStoredJSON(storage, key, value) {
  try {
    storage.setItem(key, JSON.stringify(value));
  } catch {
    // Private browsing and a full quota both land here. The ping may be sent
    // again on the next load; that is a better failure than a thrown error.
  }
}

/*
 * Whether this browser owes a ping. A version change always reports: "which
 * version is installed" is the question the data is for, and a deployment that
 * upgrades twice in one day is worth two data points.
 */
export function installPingDue(previous, now, intervalMs, version) {
  if (!previous || typeof previous.at !== "number") {
    return true;
  }
  if (String(previous.version || "") !== String(version || "")) {
    return true;
  }
  return now - previous.at >= intervalMs;
}

// What this browser last reported, or null. The About page prints it so the
// switch is not the only thing an operator can see about the ping.
export function lastInstallPing(storage = window.localStorage) {
  const record = readStoredJSON(storage, INSTALL_PING_KEY);
  if (!record || typeof record.at !== "number") {
    return null;
  }
  return { at: record.at, version: String(record.version || "") };
}

/*
 * The request for one provider.
 *
 *   custom - POST the payload as JSON.
 *   ga4    - POST a Measurement Protocol body. The endpoint already carries
 *            measurement_id and api_secret in its query string; see
 *            docs/MONITOR_GUIDE.md for how to build one.
 *   pixel  - GET an image whose path ends in the version, which is what a
 *            GoatCounter-style counter turns into a per-version breakdown.
 */
export function buildInstallPing(settings, about, installID) {
  const endpoint = String(settings?.endpoint || "").trim();
  if (!settings?.enabled || !endpoint || !installID) {
    return null;
  }
  const version = String(about?.version || "").trim() || "dev";
  const commit = String(about?.commit || "").trim();
  const platform = `${about?.goos || ""}/${about?.goarch || ""}`.replace(/^\/|\/$/g, "");
  const provider = String(settings.provider || "custom").toLowerCase();

  if (provider === "pixel") {
    const prefix = String(settings.path_prefix || "/trajecta/install").replace(/\/+$/, "");
    const url = new URL(endpoint);
    url.searchParams.set("p", `${prefix}/${version}`);
    url.searchParams.set("t", `Trajecta install ${version}`);
    return { kind: "pixel", url: url.toString() };
  }

  if (provider === "ga4") {
    return {
      kind: "beacon",
      url: endpoint,
      body: JSON.stringify({
        client_id: installID,
        events: [
          {
            name: "install_ping",
            params: {
              version,
              commit,
              platform,
              // GA4 ignores an event with no engagement signal in some reports.
              engagement_time_msec: 1,
            },
          },
        ],
      }),
    };
  }

  return {
    kind: "beacon",
    url: endpoint,
    body: JSON.stringify({
      event: "install_ping",
      install_id: installID,
      version,
      commit,
      os: about?.goos || "",
      arch: about?.goarch || "",
      ts: new Date().toISOString(),
    }),
  };
}

/*
 * Deliver one ping.
 *
 * sendBeacon is preferred because the page may be unloading and because a string
 * body goes out as `text/plain;charset=UTF-8` - a CORS-safelisted content type,
 * so there is no preflight and a collector that only knows how to accept a POST
 * still works. The response is never read either way: this is a report, not a
 * request whose answer matters.
 */
function sendInstallPing(ping, { navigator: navigatorRef = window.navigator, fetch: fetchRef = window.fetch } = {}) {
  if (ping.kind === "pixel") {
    const image = new Image();
    image.referrerPolicy = "no-referrer";
    image.src = ping.url;
    return true;
  }
  try {
    if (navigatorRef?.sendBeacon && navigatorRef.sendBeacon(ping.url, ping.body)) {
      return true;
    }
  } catch {
    // A malformed URL or a blocked beacon: fall through to fetch.
  }
  try {
    const promise = fetchRef(ping.url, {
      method: "POST",
      body: ping.body,
      keepalive: true,
      // The response is not read, so there is no reason to require CORS headers.
      mode: "no-cors",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    promise?.catch?.(() => {});
  } catch {
    return false;
  }
  return true;
}

/*
 * Report this installation, if the deployment has asked for it and this browser
 * has not reported recently.
 *
 * It resolves /api/about through the console's shared query cache, so a page
 * load still makes exactly one request for it however many readers want it.
 * Every failure is swallowed: a Monitor that cannot reach its collector behaves
 * exactly like one that has telemetry off.
 */
export async function reportInstall({
  storage = window.localStorage,
  now = Date.now(),
  send = sendInstallPing,
} = {}) {
  let about;
  try {
    about = await queryClient.fetchQuery({
      queryKey: ABOUT_QUERY_KEY,
      queryFn: () => requestJSON(apiPaths.about),
    });
  } catch {
    return false;
  }

  const settings = about?.telemetry;
  const version = String(about?.version || "").trim() || "dev";
  const intervalMs = Number(settings?.interval_seconds || 0) * 1000 || 24 * 60 * 60 * 1000;
  if (!installPingDue(readStoredJSON(storage, INSTALL_PING_KEY), now, intervalMs, version)) {
    return false;
  }

  // No id, no report: the deployment's identity is what makes the counts mean
  // "installations", and a substitute would answer a different question.
  const installID = String(about?.install_id || "").trim();
  const ping = buildInstallPing(settings, about, installID);
  if (!ping) {
    return false;
  }

  // Recorded before the send, so a collector that is down is retried tomorrow
  // rather than on every page load.
  const record = { at: now, version };
  writeStoredJSON(storage, INSTALL_PING_KEY, record);
  const sent = send(ping);
  // The About page prints "last reported by this browser", and it mounts before
  // this resolves. Announcing the report lets it update without a poll.
  if (sent && typeof window !== "undefined" && typeof window.dispatchEvent === "function") {
    window.dispatchEvent(new CustomEvent(INSTALL_PING_EVENT, { detail: record }));
  }
  return sent;
}
