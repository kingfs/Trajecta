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
 * why the list is nine items instead of twelve.
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
      { to: "/providers", labelKey: "nav.providers", icon: "plug" },
      { to: "/models", labelKey: "nav.models", icon: "box" },
      { to: "/routing", labelKey: "nav.routing", icon: "route" },
      { to: "/connect", labelKey: "nav.access", icon: "terminal" },
    ],
  },
  {
    id: "system",
    labelKey: "nav.group.system",
    items: [{ to: "/system", labelKey: "nav.system", icon: "settings", adminOnly: true }],
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
 * /requests, /sessions, /analysis and /tokens are the pre-redesign addresses of
 * pages that are now tabs of another entry. They redirect, but a redirect still
 * has to light up the entry it lands on while it is in flight.
 */
export function isLegacyActive(path) {
  if (typeof window === "undefined") {
    return false;
  }
  const current = window.location.pathname;
  if (path === "/traces") {
    return current === "/requests" || current === "/sessions" || current.startsWith("/traces/");
  }
  if (path === "/audit") {
    return current === "/analysis" || current.startsWith("/audit");
  }
  if (path === "/connect") {
    return current === "/tokens";
  }
  return false;
}

/*
 * Which rail entry a URL belongs to, including the pre-redesign aliases and the
 * detail routes that hang off a list page (/traces/:id belongs to 流量,
 * /providers/:id belongs to 模型服务商). The longest matching prefix wins, so
 * /models/foo does not also match /models.
 */
export function navItemForPath(pathname) {
  const path = String(pathname || "/");
  let match = null;
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      if (path === item.to || path.startsWith(`${item.to}/`) || isLegacyActive(item.to)) {
        if (!match || item.to.length > match.item.to.length) {
          match = { group, item };
        }
      }
    }
  }
  return match;
}
