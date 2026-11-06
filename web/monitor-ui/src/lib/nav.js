/*
 * The navigation model, in one place.
 *
 * It used to live inside PrimaryNav, which was fine while the rail was the only
 * thing that rendered it. The page header now names the group a page belongs to,
 * and that label has to come from the same declaration the rail uses - a second
 * copy would be a second thing to keep in step with the routes.
 *
 * The grouping is by what the operator is doing, not by which table the data
 * comes from: 监控 answers "what is happening", 配置 answers "what is it pointed
 * at", 系统 answers "is the machine healthy". Pages that used to be separate
 * destinations now live behind the tab strip of one of these entries, which is
 * why the list is nine items. 路由 was the last standalone page to fold in: its
 * settings and dry-run inspector are now the channel page's advanced cards, and
 * the alias table moved to 模型, so the rail has no routing entry left to draw.
 * The ninth item is 关于: it is drawn in the 系统 group because that is where a
 * reader looks for "which build is this", but unlike 系统 itself it carries
 * nothing an admin has to guard.
 */
export const NAV_GROUPS = [
  {
    id: "observe",
    labelKey: "nav.group.observe",
    items: [
      { to: "/overview", labelKey: "nav.overview", icon: "grid" },
      { to: "/traces", labelKey: "nav.traffic", icon: "activity" },
      { to: "/events", labelKey: "nav.events", icon: "bell", badge: "events" },
      { to: "/audit", labelKey: "nav.quality", icon: "shield" },
    ],
  },
  {
    id: "configure",
    labelKey: "nav.group.configure",
    items: [
      // /channels is the canonical address. /providers is only a redirect kept
      // for bookmarks, and /routing was the page whose controls now live inside
      // this one; both are listed as legacy paths so the entry keeps lighting up
      // while the redirect is in flight and the palette never offers a dead
      // destination.
      { to: "/channels", labelKey: "nav.providers", icon: "plug", legacyPaths: ["/providers", "/routing"] },
      { to: "/models", labelKey: "nav.models", icon: "box" },
      { to: "/connect", labelKey: "nav.access", icon: "terminal" },
    ],
  },
  {
    id: "system",
    labelKey: "nav.group.system",
    items: [
      { to: "/system", labelKey: "nav.system", icon: "settings", adminOnly: true },
      // The About page sits beside the system page but is not part of it: it
      // reports the build rather than the machine, so it draws no admin-only
      // data and every signed-in role may open it.
      { to: "/about", labelKey: "nav.about", icon: "info" },
    ],
  },
];

// The system page exposes process internals, database statistics and (when the
// slow-query collector is armed) statement text, so the API behind it is
// admin-only. The entry follows the same rule: an admin sees it, and so does
// the "local" pseudo-user of a Monitor running without auth, which is the only
// deployment whose API answers without a role.
export function canOpenSystemPage(user) {
  const role = String(user?.role || "").trim().toLowerCase();
  return role === "admin" || role === "local";
}

export function navGroups(user) {
  const systemAllowed = canOpenSystemPage(user);
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => !item.adminOnly || systemAllowed),
  })).filter((group) => group.items.length > 0);
}

/*
 * The pre-relocation addresses of entries that now live elsewhere: /requests,
 * /sessions, /analysis and /tokens are old addresses of merged tabs, and
 * /providers and /routing are the old addresses of the channel page. They
 * redirect, but a redirect still has to light up the entry it lands on while it
 * is in flight.
 */
const LEGACY_ACTIVE_PATHS = {
  "/traces": ["/requests", "/sessions"],
  "/audit": ["/analysis"],
  "/connect": ["/tokens"],
  "/channels": ["/providers", "/routing"],
};

function pathMatches(path, prefix) {
  return path === prefix || path.startsWith(`${prefix}/`);
}

export function isLegacyActive(to) {
  if (typeof window === "undefined") {
    return false;
  }
  const current = window.location.pathname;
  // Detail routes hang off a list page (/traces/:id belongs to 流量,
  // /channels/:id belongs to 渠道) and are matched by the same prefix rule.
  if (pathMatches(current, to)) {
    return true;
  }
  return (LEGACY_ACTIVE_PATHS[to] || []).some((alias) => pathMatches(current, alias));
}

/*
 * Which rail entry a URL belongs to, including the pre-relocation aliases and
 * the detail routes that hang off a list page. Every item may name extra
 * `legacyPaths` that belong to it without being a destination of their own. The
 * longest matching prefix wins, so /models/foo does not also match /models.
 */
export function navItemForPath(pathname) {
  const path = String(pathname || "/");
  let match = null;
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      const belongs = pathMatches(path, item.to) || (item.legacyPaths || []).some((legacy) => pathMatches(path, legacy));
      if (belongs && (!match || item.to.length > match.item.to.length)) {
        match = { group, item };
      }
    }
  }
  return match;
}
