import React, { useEffect, useState } from "react";
import { NavLink } from "react-router-dom";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "./ui/dialog";
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
import { applyTheme, currentTheme, THEME_KEY, themeOptions } from "../lib/theme";

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
  const [preferencesOpen, setPreferencesOpen] = useState(false);
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

      {/* One Dialog root wraps the rail button and the panel it opens. Radix
          hands focus back to the trigger on close, which only works when the two
          share a root. */}
      <Dialog open={accountOpen} onOpenChange={setAccountOpen}>
      {/* The trigger is a Radix DialogTrigger rather than a plain button with an
          onClick. That is what lets the dialog hand focus back to the button it
          was opened from when it closes; without it focus lands on <body> and a
          keyboard reader loses their place in the rail. */}
      <div className="nav-account">
        <DialogTrigger asChild>
          <button
            className="account-trigger"
            type="button"
            aria-expanded={accountOpen}
            aria-label={t("account.openAccount")}
            title={collapsed ? displayName(user) : undefined}
          >
            <span className="account-avatar">{initials(user)}</span>
            <span className="account-copy">
              <strong>{displayName(user)}</strong>
              <small>{user?.role || t("account.roleFallback")}</small>
            </span>
          </button>
        </DialogTrigger>
      </div>

      {accountOpen ? (
        <AccountDialog
          user={user}
          onLogout={onLogout}
          onPreferences={() => {
            setPreferencesOpen(true);
            setAccountOpen(false);
          }}
          onPassword={() => {
            setPasswordOpen(true);
            setAccountOpen(false);
          }}
        />
      ) : null}
      </Dialog>

      {preferencesOpen ? <PreferencesDialog onClose={() => setPreferencesOpen(false)} /> : null}
      {passwordOpen ? <PasswordDialog onClose={() => setPasswordOpen(false)} /> : null}
    </nav>
  );
}

/**
 * The account surface is a centred dialog rather than a popover.
 *
 * Anchored to the trigger at the bottom of the rail, its content was clamped to
 * the sidebar's own width, which left the language picker and the account fields
 * fighting for ~200px. The modal reuses the same backdrop the preferences and
 * password dialogs already use, so the sidebar keeps one overlay pattern.
 */
function AccountDialog({ user, onLogout, onPreferences, onPassword }) {
  const { t } = useI18n();
  // Radix owns Escape, the backdrop click, the focus trap, focus restore and the
  // body scroll lock, so the document-level keydown listener this used to add is
  // gone. The Dialog root lives in PrimaryNav, wrapping both this and the
  // trigger, because that pairing is what makes focus restore work.
  return (
    <DialogContent className="account-modal" aria-labelledby="account-title">
        <DialogHeader>
          <div className="account-modal-identity">
            <span className="account-avatar account-avatar-menu">{initials(user)}</span>
            <div>
              <DialogTitle id="account-title">{displayName(user)}</DialogTitle>
              <span className="account-modal-role">
                {user?.role || t("account.roleFallback")} · {user?.scope || t("account.scopeFallback")}
              </span>
            </div>
          </div>
          <DialogClose asChild>
            <button className="icon-button" type="button" aria-label={t("common.close")}>
              <X size={14} aria-hidden="true" />
            </button>
          </DialogClose>
        </DialogHeader>
      <AccountMenuContent onLogout={onLogout} onPreferences={onPreferences} onPassword={onPassword} />
    </DialogContent>
  );
}

function AccountMenuContent({ onLogout, onPreferences, onPassword }) {
  const { t } = useI18n();
  return (
    <>
      <button className="account-menu-item" type="button" onClick={onPreferences}>
        <NavIcon name="settings" />
        <span>{t("account.preferences")}</span>
      </button>
      <button className="account-menu-item" type="button" onClick={onPassword}>
        <NavIcon name="lock" />
        <span>{t("account.changePassword")}</span>
      </button>
      <div className="account-menu-divider" />
      <button className="account-menu-item account-menu-danger" type="button" onClick={onLogout}>
        <NavIcon name="logout" />
        <span>{t("account.signOut")}</span>
      </button>
    </>
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

function ThemeSwitcher({ labelled = false }) {
  const { t } = useI18n();
  const [theme, setTheme] = useState(() => currentTheme());

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  return (
    <div className={labelled ? "theme-switcher theme-switcher-labelled" : "theme-switcher"} role="group" aria-label={t("preferences.theme")}>
      {themeOptions.map((option) => (
        <button
          key={option.value}
          className={theme === option.value ? "theme-option theme-option-active" : "theme-option"}
          type="button"
          title={t(`theme.${option.value}`)}
          aria-label={t(`theme.${option.value}`)}
          aria-pressed={theme === option.value}
          onClick={() => {
            window.localStorage.setItem(THEME_KEY, option.value);
            setTheme(option.value);
          }}
        >
          <span className={`theme-dot theme-dot-${option.value}`} aria-hidden="true" />
          <span className="theme-option-text">{labelled ? t(`theme.${option.value}`) : option.short}</span>
        </button>
      ))}
    </div>
  );
}

function LanguageSwitcher() {
  const { language, setLanguage, t } = useI18n();
  return (
    <div className="language-switcher" role="group" aria-label={t("preferences.language")}>
      {languageOptions.map((option) => (
        <button
          key={option.value}
          className={language === option.value ? "language-option language-option-active" : "language-option"}
          type="button"
          aria-pressed={language === option.value}
          onClick={() => setLanguage(option.value)}
        >
          <span>{option.label}</span>
        </button>
      ))}
    </div>
  );
}

function PreferencesDialog({ onClose }) {
  const { t } = useI18n();
  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="preferences-modal" aria-labelledby="preferences-title">
        <DialogHeader>
          <div>
            <p className="eyebrow">{t("preferences.eyebrow")}</p>
            <DialogTitle id="preferences-title">{t("preferences.title")}</DialogTitle>
          </div>
          <DialogClose asChild>
            <button className="icon-button" type="button" aria-label={t("common.close")}>
              <X size={14} aria-hidden="true" />
            </button>
          </DialogClose>
        </DialogHeader>
        <div className="preferences-list">
          <section className="preferences-row">
            <div>
              <strong>{t("preferences.language")}</strong>
              <span>{t("preferences.saved")}</span>
            </div>
            <LanguageSwitcher />
          </section>
          <section className="preferences-row">
            <div>
              <strong>{t("preferences.theme")}</strong>
              <span>{t("preferences.saved")}</span>
            </div>
            <ThemeSwitcher labelled />
          </section>
        </div>
        <DialogFooter>
          <button className="ghost-button" type="button" onClick={onClose}>
            {t("preferences.close")}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PasswordDialog({ onClose }) {
  const { t } = useI18n();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");

  const submit = async (event) => {
    event.preventDefault();
    setError("");
    setStatus("");
    try {
      await postJSON(apiPaths.authPassword, { current_password: currentPassword, new_password: newPassword });
      setCurrentPassword("");
      setNewPassword("");
      setStatus(t("password.updated"));
    } catch (err) {
      setError(err.message || t("password.failed"));
    }
  };

  // The form sits inside the dialog rather than being the dialog element itself:
  // Radix clones its content element when `asChild` is used, and a form as the
  // content root bought nothing.
  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent aria-labelledby="password-title">
        <form onSubmit={submit}>
          <DialogHeader>
            <div>
              <p className="eyebrow">{t("password.eyebrow")}</p>
              <DialogTitle id="password-title">{t("password.title")}</DialogTitle>
            </div>
            <DialogClose asChild>
              <button className="icon-button" type="button" aria-label={t("common.close")}>
                <X size={14} aria-hidden="true" />
              </button>
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
          {error ? <p className="auth-error">{error}</p> : null}
          {status ? <p className="auth-success">{status}</p> : null}
          <DialogFooter>
            <button className="ghost-button" type="button" onClick={onClose}>
              {t("password.cancel")}
            </button>
            <button className="ghost-button active" type="submit">
              {t("password.update")}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
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
