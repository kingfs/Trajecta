import React, { useEffect, useState } from "react";
import { Button } from "./ui/button";
import { NavLink } from "react-router-dom";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { RadioGroup, RadioGroupItem } from "./ui/radio-group";
import { Separator } from "./ui/separator";
import {
  Activity,
  Bell,
  LayoutGrid,
  Lock,
  LogOut,
  Package,
  PanelLeft,
  Plug,
  Route,
  Settings,
  Shield,
  Terminal,
  X,
} from "lucide-react";
import { apiPaths, apiURL, postJSON, requestJSON, streamSystemEvents } from "../lib/api";
import { languageOptions, useI18n } from "../lib/i18n";
import { useWriteMutation } from "../lib/mutations";
import { applyTheme, currentTheme, THEME_KEY, themeOptions } from "../lib/theme";
import { cn } from "../lib/utils";

/**
 * The sidebar is grouped by what the operator is doing, not by which table the
 * data comes from: 监控 answers "what is happening", 配置 answers "what is it
 * pointed at", 系统 answers "is the machine healthy". Pages that used to be
 * separate destinations now live behind the tab strip of one of these entries,
 * which is why the list is nine items instead of twelve.
 */
const NAV_GROUPS = [
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
function canOpenSystemPage(user) {
  const role = String(user?.role || "").trim().toLowerCase();
  return role === "admin" || role === "local";
}

function navGroups(user) {
  const systemAllowed = canOpenSystemPage(user);
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => !item.adminOnly || systemAllowed),
  })).filter((group) => group.items.length > 0);
}

export function PrimaryNav({ user, onLogout, collapsed = false, onToggleCollapsed }) {
  const { t } = useI18n();
  const [accountOpen, setAccountOpen] = useState(false);
  // The account button, as the place focus returns to after a form opened from
  // the panel closes. See PasswordDialog.
  const accountButtonRef = React.useRef(null);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [eventSummary, setEventSummary] = useState(null);

  useEffect(() => {
    let cancelled = false;
    let timer = 0;
    let source = null;
    const refresh = async () => {
      try {
        const payload = await requestJSON(apiURL(apiPaths.eventsSummary, { window: "all" }));
        if (!cancelled) {
          setEventSummary(payload);
        }
      } catch {
        if (!cancelled) {
          setEventSummary(null);
        }
      }
    };
    refresh();
    const onRefresh = () => refresh();
    window.addEventListener("trajecta:events-refresh", onRefresh);
    // The stream carries the JWT in an Authorization header (see
    // streamSystemEvents): putting it in the query string leaked it into every
    // reverse-proxy access log.
    source = streamSystemEvents({
      onEvent: (event) => {
        if (event.event !== "system_event.summary" && event.event !== "system_event.updated") {
          return;
        }
        try {
          const payload = JSON.parse(event.data || "{}");
          setEventSummary((current) => ({
            ...(current || {}),
            unread: Number(payload.unread || 0),
          }));
        } catch {
          refresh();
        }
      },
      onError: () => refresh(),
    });
    timer = window.setInterval(refresh, 60_000);
    return () => {
      cancelled = true;
      window.removeEventListener("trajecta:events-refresh", onRefresh);
      if (source) {
        source();
      }
      window.clearInterval(timer);
    };
  }, []);

  return (
    <nav className="primary-nav" aria-label={t("nav.primary")}>
      <div className="nav-brand">
        <div className="nav-brand-copy">
          <span className="nav-brand-mark" aria-hidden="true">T</span>
          <strong>Trajecta</strong>
        </div>
        <button
          className="sidebar-toggle"
          type="button"
          onClick={onToggleCollapsed}
          aria-label={collapsed ? t("nav.expandSidebar") : t("nav.collapseSidebar")}
          title={collapsed ? t("nav.expandSidebar") : t("nav.collapseSidebar")}
        >
          <NavIcon name={collapsed ? "sidebar" : "sidebar-collapse"} />
        </button>
      </div>

      <div className="nav-scroll">
        {navGroups(user).map((group) => (
          <div className="nav-section" key={group.id}>
            <div className="nav-section-label">{t(group.labelKey)}</div>
            {group.items.map((item) => {
              const label = t(item.labelKey);
              return (
                <NavLink
                  key={item.to}
                  to={item.to}
                  title={collapsed ? label : undefined}
                  className={({ isActive }) => (isActive || isLegacyActive(item.to) ? "nav-item nav-item-active" : "nav-item")}
                >
                  <NavIcon name={item.icon} />
                  <span className="nav-item-label">{label}</span>
                  {item.badge === "events" && Number(eventSummary?.unread || 0) > 0 ? (
                    <span className="nav-item-badge">{formatBadgeCount(eventSummary.unread)}</span>
                  ) : null}
                </NavLink>
              );
            })}
          </div>
        ))}
      </div>

      {/* One popover root wraps the rail button and the panel it opens. Radix
          portals the panel and positions it against the button, which is what
          lets it hang over the page instead of being clamped to the rail's
          width, and it hands focus back to the button on close, which only
          works when the two share a root. */}
      <Popover open={accountOpen} onOpenChange={setAccountOpen}>
        <div className="nav-account">
          <PopoverTrigger asChild>
            <button
              ref={accountButtonRef}
              className="account-trigger"
              type="button"
              aria-label={t("account.openAccount")}
              title={collapsed ? displayName(user) : undefined}
            >
              <span className="account-avatar">{initials(user)}</span>
              <span className="account-copy">
                <strong>{displayName(user)}</strong>
                <small>{user?.role || t("account.roleFallback")}</small>
              </span>
            </button>
          </PopoverTrigger>
        </div>

        <AccountPanel
          user={user}
          onLogout={onLogout}
          onPassword={() => {
            setPasswordOpen(true);
            setAccountOpen(false);
          }}
        />
      </Popover>

      {passwordOpen ? <PasswordDialog returnFocusTo={accountButtonRef} onClose={() => setPasswordOpen(false)} /> : null}
    </nav>
  );
}

/**
 * The account surface is a panel that opens upwards from the rail button.
 *
 * It was a centred dialog before that, and a hand-positioned popover clamped to
 * the rail before that. Neither is what this is: surface attached to the button
 * that opened it, hanging over the page so its content is measured against the
 * viewport rather than against the 232px rail. Radix flips it when there is no
 * room above, so the same markup works with the rail collapsed and on a phone.
 *
 * The theme and the language live here rather than behind a Preferences dialog.
 * Each is one choice out of several, so each is a radio group, and neither is a
 * page-level decision that deserves a modal: a reader who wants the light theme
 * should not have to open one to get it. The panel stays open across a pick, so
 * both can be set before it is dismissed, and the arrows move within a group
 * instead of walking out of the panel.
 */
// One row of the account panel. Utilities rather than a legacy class so the row
// and the radio items above it hover the same way.
const PANEL_ACTION =
  "flex w-full cursor-pointer items-center gap-3 rounded-md px-2 py-2 font-sans text-sm text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring";

export function AccountPanel({ user, onLogout, onPassword }) {
  const { t } = useI18n();
  return (
    <PopoverContent side="top" align="start" sideOffset={10} aria-label={t("account.openAccount")} className="w-[272px]">
      <div className="flex min-w-0 items-center gap-3 px-2 py-1.5">
        <span className="account-avatar size-[34px] text-sm">{initials(user)}</span>
        <div className="min-w-0 flex-1">
          <strong className="block truncate font-sans text-sm font-medium text-foreground">{displayName(user)}</strong>
          <span className="block truncate text-label text-faint">
            {user?.role || t("account.roleFallback")} · {user?.scope || t("account.scopeFallback")}
          </span>
        </div>
      </div>
      <Separator className="my-1.5" />
      <p className="px-2 pt-1 pb-1 font-sans text-label font-medium text-faint">{t("preferences.theme")}</p>
      <ThemePicker />
      <Separator className="my-1.5" />
      <p className="px-2 pt-1 pb-1 font-sans text-label font-medium text-faint">{t("preferences.language")}</p>
      <LanguagePicker />
      <Separator className="my-1.5" />
      <button className={PANEL_ACTION} type="button" onClick={onPassword}>
        <NavIcon name="lock" />
        <span>{t("account.changePassword")}</span>
      </button>
      <button className={cn(PANEL_ACTION, "text-danger hover:text-danger")} type="button" onClick={onLogout}>
        <NavIcon name="logout" />
        <span>{t("account.signOut")}</span>
      </button>
    </PopoverContent>
  );
}

function ThemePicker() {
  const { t } = useI18n();
  const [theme, setTheme] = useState(() => currentTheme());

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  return (
    <RadioGroup
      value={theme}
      aria-label={t("preferences.theme")}
      onValueChange={(next) => {
        window.localStorage.setItem(THEME_KEY, next);
        setTheme(next);
      }}
    >
      {themeOptions.map((option) => (
        <RadioGroupItem key={option.value} value={option.value}>
          <span className={`theme-dot theme-dot-${option.value}`} aria-hidden="true" />
          <span>{t(`theme.${option.value}`)}</span>
        </RadioGroupItem>
      ))}
    </RadioGroup>
  );
}

function LanguagePicker() {
  const { language, setLanguage, t } = useI18n();
  return (
    <RadioGroup value={language} aria-label={t("preferences.language")} onValueChange={setLanguage}>
      {languageOptions.map((option) => (
        <RadioGroupItem key={option.value} value={option.value}>
          <span>{option.label}</span>
        </RadioGroupItem>
      ))}
    </RadioGroup>
  );
}

function PasswordDialog({ onClose, returnFocusTo }) {
  const { t } = useI18n();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const changePassword = useWriteMutation({
    mutationFn: () => postJSON(apiPaths.authPassword, { current_password: currentPassword, new_password: newPassword }),
    success: "password.updated",
    error: "password.failed",
    onSuccess: () => {
      setCurrentPassword("");
      setNewPassword("");
    },
  });

  const submit = (event) => {
    event.preventDefault();
    changePassword.mutate();
  };

  // The form sits inside the dialog rather than being the dialog element itself:
  // Radix clones its content element when `asChild` is used, and a form as the
  // content root bought nothing.
  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent
        aria-labelledby="password-title"
        onCloseAutoFocus={(event) => {
          // Radix hands focus back to the trigger it owns. This form has none -
          // it is opened from the account panel, so the menu item that opened it
          // is already unmounted - and without this the reader would be dropped
          // on <body> at the top of the document.
          event.preventDefault();
          returnFocusTo?.current?.focus();
        }}
      >
        <form onSubmit={submit}>
          <DialogHeader>
            <div>
              <p className="eyebrow">{t("password.eyebrow")}</p>
              <DialogTitle id="password-title">{t("password.title")}</DialogTitle>
            </div>
            <DialogClose asChild>
              <Button variant="default" size="icon" type="button" aria-label={t("common.close")}>
                <X size={14} aria-hidden="true" />
              </Button>
            </DialogClose>
          </DialogHeader>
          <label className="nav-field">
            {t("password.current")}
            <input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
          </label>
          <label className="nav-field">
            {t("password.next")}
            <input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
          </label>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>
              {t("password.cancel")}
            </Button>
            <Button variant="primary" type="submit">
              {t("password.update")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/*
 * Navigation glyphs, from lucide-react. The thirteen cases that used to be
 * hand-drawn 24x24 paths are one word each now, and they match the icons the
 * rest of the console uses.
 *
 * The sidebar toggle is the one case that needs a decision rather than a
 * lookup: lucide draws a single PanelLeft whose chevron always points right, so
 * the expanded rail and the collapsed rail would look identical. The two states
 * are mirrored with a horizontal flip, which is what the hand-drawn pair did.
 */
const NAV_ICONS = {
  sidebar: PanelLeft,
  grid: LayoutGrid,
  bell: Bell,
  activity: Activity,
  shield: Shield,
  route: Route,
  box: Package,
  plug: Plug,
  terminal: Terminal,
  lock: Lock,
  logout: LogOut,
  settings: Settings,
};

function NavIcon({ name }) {
  if (name === "sidebar-collapse") {
    return <PanelLeft size={15} className="-scale-x-100" aria-hidden="true" />;
  }
  const Icon = NAV_ICONS[name];
  if (!Icon) {
    return null;
  }
  return <Icon size={15} aria-hidden="true" />;
}

function displayName(user) {
  return user?.username || "Monitor user";
}

function initials(user) {
  return displayName(user).slice(0, 2).toUpperCase();
}

function formatBadgeCount(value) {
  const count = Number(value || 0);
  return count > 99 ? "99+" : String(count);
}

// /requests, /sessions, /analysis and /tokens are the pre-redesign addresses of
// pages that are now tabs of another entry. They redirect, but a redirect still
// has to light up the entry it lands on while it is in flight.
function isLegacyActive(path) {
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
