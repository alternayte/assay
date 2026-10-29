import type { QueryClient } from "@tanstack/react-query";
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  Link,
  Outlet,
  redirect,
} from "@tanstack/react-router";
import { Shell } from "@/components/shell";
import { Button } from "@/components/ui/button";
import { t } from "@/copy";
import { atLeast, meQuery } from "@/lib/session";
import { ProfilePage } from "@/routes/profile";
import { InvitePage, PublicPage, ResetPage, SetupPage, SignInPage } from "@/routes/public";
import { DiagnosticsPage } from "@/routes/settings/diagnostics";
import { GroupsPage } from "@/routes/settings/groups";
import { AuditPage, EmailPage, GeneralPage, SettingsLayout } from "@/routes/settings/index";
import { ModelsPage } from "@/routes/settings/models";
import { UsersPage } from "@/routes/settings/users";

type Context = { queryClient: QueryClient };

function NotFound() {
  return (
    <PublicPage title={t("app.notFoundTitle")} body={t("app.notFoundBody")}>
      <Button asChild variant="primary">
        <Link to="/">{t("app.notFoundAction")}</Link>
      </Button>
    </PublicPage>
  );
}

function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <PublicPage title={t("app.error")}>
      <Button variant="primary" onClick={reset}>
        {t("app.retry")}
      </Button>
    </PublicPage>
  );
}

const root = createRootRouteWithContext<Context>()({
  component: Outlet,
  notFoundComponent: NotFound,
  errorComponent: ErrorPage,
});

const signIn = createRoute({ getParentRoute: () => root, path: "/sign-in", component: SignInPage });
const setup = createRoute({ getParentRoute: () => root, path: "/setup", component: SetupPage });
const invite = createRoute({ getParentRoute: () => root, path: "/invite", component: InvitePage });
const reset = createRoute({ getParentRoute: () => root, path: "/reset-password", component: ResetPage });

const authed = createRoute({
  getParentRoute: () => root,
  id: "authed",
  component: Shell,
  beforeLoad: async ({ context, location }) => {
    const me = await context.queryClient.ensureQueryData(meQuery);
    if (!me) throw redirect({ to: "/sign-in", search: { next: location.href } });
    return { me };
  },
});

const home = createRoute({
  getParentRoute: () => authed,
  path: "/",
  beforeLoad: ({ context }) => {
    throw redirect({ to: atLeast(context.me, "admin") ? "/settings/users" : "/profile" });
  },
});

const profile = createRoute({ getParentRoute: () => authed, path: "/profile", component: ProfilePage });

const settings = createRoute({
  getParentRoute: () => authed,
  path: "/settings",
  component: SettingsLayout,
  beforeLoad: ({ context }) => {
    if (!atLeast(context.me, "admin")) throw redirect({ to: "/profile" });
  },
});
const settingsIndex = createRoute({
  getParentRoute: () => settings,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/users" });
  },
});
const users = createRoute({ getParentRoute: () => settings, path: "/users", component: UsersPage });
const groups = createRoute({ getParentRoute: () => settings, path: "/groups", component: GroupsPage });
const general = createRoute({ getParentRoute: () => settings, path: "/general", component: GeneralPage });
const email = createRoute({ getParentRoute: () => settings, path: "/email", component: EmailPage });
const audit = createRoute({ getParentRoute: () => settings, path: "/audit", component: AuditPage });
const modelsRoute = createRoute({ getParentRoute: () => settings, path: "/models", component: ModelsPage });
const diagnostics = createRoute({
  getParentRoute: () => settings,
  path: "/diagnostics",
  component: DiagnosticsPage,
});

const routeTree = root.addChildren([
  signIn,
  setup,
  invite,
  reset,
  authed.addChildren([
    home,
    profile,
    settings.addChildren([settingsIndex, users, groups, general, modelsRoute, email, diagnostics, audit]),
  ]),
]);

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({ routeTree, context: { queryClient }, defaultPreload: "intent" });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
