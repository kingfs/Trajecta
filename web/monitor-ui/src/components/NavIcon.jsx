import React from "react";
import {
  Activity,
  Bell,
  Info,
  Languages,
  LayoutGrid,
  Lock,
  LogOut,
  Package,
  PanelLeft,
  Plug,
  RefreshCw,
  Route,
  Settings,
  Shield,
  SunMoon,
  Terminal,
} from "lucide-react";

/*
 * The rail's destination glyphs, in one map.
 *
 * The quick-navigation palette renders the same destinations, so the map lives
 * here rather than inside PrimaryNav: two lists of icons would be two things to
 * keep in step, and the palette is exactly the surface where a mismatched glyph
 * is most visible - the reader is searching by name, not by position.
 */
export const NAV_ICONS = {
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
  info: Info,
  refresh: RefreshCw,
  theme: SunMoon,
  language: Languages,
};

/*
 * The sidebar toggle is the one case that needs a decision rather than a
 * lookup: lucide draws a single PanelLeft whose chevron always points right, so
 * the expanded rail and the collapsed rail would look identical. The two states
 * are mirrored with a horizontal flip, which is what the hand-drawn pair did.
 */
export function NavIcon({ name, size = 15, className }) {
  if (name === "sidebar-collapse") {
    return <PanelLeft size={size} className={className ? `${className} -scale-x-100` : "-scale-x-100"} aria-hidden="true" />;
  }
  const Icon = NAV_ICONS[name];
  if (!Icon) {
    return null;
  }
  return <Icon size={size} className={className} aria-hidden="true" />;
}
