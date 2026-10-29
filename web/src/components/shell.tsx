import { Link, Outlet } from "@tanstack/react-router";
import { LogOut, Monitor, Moon, Search, Sun, UserRound } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { useState } from "react";
import { Palette } from "@/components/palette";
import { Button } from "@/components/ui/button";
import { t } from "@/copy";
import { atLeast, useMe, useSignOut } from "@/lib/session";
import { setTheme, storedTheme, type ThemeChoice } from "@/lib/theme";

const themes: { value: ThemeChoice; label: Parameters<typeof t>[0]; icon: typeof Sun }[] = [
  { value: "light", label: "nav.themeLight", icon: Sun },
  { value: "dark", label: "nav.themeDark", icon: Moon },
  { value: "system", label: "nav.themeSystem", icon: Monitor },
];

const navLink =
  "rounded-sm px-2 py-1 text-base text-ink-muted transition-ink hover:bg-paper-sunk hover:text-ink data-[status=active]:text-ink data-[status=active]:shadow-[inset_0_-2px_0_var(--accent)]";

export function Shell() {
  const me = useMe();
  const signOut = useSignOut();
  const [palette, setPalette] = useState(false);
  const [theme, setThemeState] = useState<ThemeChoice>(storedTheme());

  const choose = (v: ThemeChoice) => {
    setTheme(v);
    setThemeState(v);
  };

  return (
    <div className="flex min-h-dvh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:bg-paper-raised focus:p-2"
      >
        {t("app.skip")}
      </a>
      <header className="sticky top-0 z-30 border-b border-rule-strong bg-paper/95 backdrop-blur">
        <div className="mx-auto flex h-12 max-w-6xl items-center gap-3 px-4">
          <Link to="/" className="flex items-baseline gap-2 font-semibold tracking-tight">
            <span aria-hidden className="font-mono text-accent">
              Au
            </span>
            <span>{t("app.name")}</span>
          </Link>
          <nav aria-label={t("nav.label")} className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
            {atLeast(me, "admin") && (
              <Link to="/settings/users" className={navLink} activeOptions={{ includeSearch: false }}>
                {t("nav.settings")}
              </Link>
            )}
          </nav>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setPalette(true)}
            aria-label={t("nav.palette")}
            className="text-ink-muted"
          >
            <Search aria-hidden />
            <span className="hidden sm:inline">{t("nav.palette")}</span>
            <kbd className="hidden rounded-sm border border-rule px-1 font-mono text-xs sm:inline">
              {t("nav.paletteShortcut")}
            </kbd>
          </Button>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="icon" aria-label={t("nav.account")}>
                <UserRound aria-hidden />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                align="end"
                sideOffset={6}
                className="z-50 min-w-56 rounded-md border border-rule-strong bg-paper-raised p-1 shadow-sheet"
              >
                <div className="border-b border-rule px-2 py-2">
                  <p className="text-base font-medium">{me.name}</p>
                  <p className="font-mono text-xs text-ink-muted">{me.email}</p>
                </div>
                <DropdownMenu.Item asChild>
                  <Link
                    to="/profile"
                    className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-base outline-none data-[highlighted]:bg-accent-wash"
                  >
                    <UserRound className="size-4" aria-hidden />
                    {t("nav.profile")}
                  </Link>
                </DropdownMenu.Item>
                <DropdownMenu.Separator className="my-1 h-px bg-rule" />
                <DropdownMenu.RadioGroup value={theme} onValueChange={(v) => choose(v as ThemeChoice)}>
                  {themes.map(({ value, label, icon: Icon }) => (
                    <DropdownMenu.RadioItem
                      key={value}
                      value={value}
                      className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-base outline-none data-[highlighted]:bg-accent-wash data-[state=checked]:font-medium data-[state=checked]:text-accent"
                    >
                      <Icon className="size-4" aria-hidden />
                      {t(label)}
                    </DropdownMenu.RadioItem>
                  ))}
                </DropdownMenu.RadioGroup>
                <DropdownMenu.Separator className="my-1 h-px bg-rule" />
                <DropdownMenu.Item
                  onSelect={signOut}
                  className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-base outline-none data-[highlighted]:bg-accent-wash"
                >
                  <LogOut className="size-4" aria-hidden />
                  {t("nav.signOut")}
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </header>
      <main id="main" className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
        <Outlet />
      </main>
      <Palette open={palette} onOpenChange={setPalette} />
    </div>
  );
}
