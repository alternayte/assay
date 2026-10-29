import type { CopyKey } from "@/copy";

export type Role = "member" | "reviewer" | "admin";

export type NavItem = { to: string; label: CopyKey; role: Role; group: "pages" | "settings" };

/** Every route that the shell and the palette can reach, with its role. */
export const navItems: NavItem[] = [
  { to: "/", label: "nav.ask", role: "member", group: "pages" },
  { to: "/explore", label: "nav.explore", role: "member", group: "pages" },
  { to: "/settings/users", label: "settings.title", role: "admin", group: "pages" },
  { to: "/profile", label: "nav.profile", role: "member", group: "pages" },
  { to: "/settings/users", label: "settings.users", role: "admin", group: "settings" },
  { to: "/settings/groups", label: "settings.groups", role: "admin", group: "settings" },
  { to: "/settings/connections", label: "settings.connections", role: "admin", group: "settings" },
  { to: "/settings/domains", label: "settings.domains", role: "admin", group: "settings" },
  { to: "/settings/general", label: "settings.general", role: "admin", group: "settings" },
  { to: "/settings/models", label: "settings.models", role: "admin", group: "settings" },
  { to: "/settings/email", label: "settings.email", role: "admin", group: "settings" },
  { to: "/settings/diagnostics", label: "settings.diagnostics", role: "admin", group: "settings" },
  { to: "/settings/audit", label: "settings.audit", role: "admin", group: "settings" },
];
