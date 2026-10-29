import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { type FormEvent, type ReactNode, useState } from "react";
import { ApiError, api, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { SkeletonRows } from "@/components/ui/skeleton";
import { t } from "@/copy";

/** The frame of a page without the shell: one sheet on the ruled ground. */
export function PublicPage({
  title,
  body,
  children,
}: {
  title: string;
  body?: string;
  children?: ReactNode;
}) {
  return (
    <main id="main" className="ruled flex min-h-dvh items-start justify-center px-4 py-[12vh]">
      <div className="w-full max-w-md rounded-md border border-rule-strong bg-paper-raised shadow-sheet">
        <header className="border-b border-rule px-5 py-4">
          <p className="flex items-baseline gap-2 font-semibold">
            <span aria-hidden className="font-mono text-accent">
              Au
            </span>
            {t("app.name")}
          </p>
          <h1 className="mt-3 text-2xl font-semibold tracking-tight">{title}</h1>
          {body && <p className="mt-1 text-base text-ink-muted">{body}</p>}
        </header>
        {children && <div className="px-5 py-4">{children}</div>}
      </div>
    </main>
  );
}

function errorText(e: unknown, map: Record<string, string>): string {
  if (e instanceof ApiError) {
    if (e.code === "rate_limited") return t("auth.rateLimited", { seconds: e.retryAfter ?? 60 });
    return map[e.code] ?? e.message;
  }
  return t("errors.network");
}

export function SignInPage() {
  const qc = useQueryClient();
  const search = useSearch({ strict: false }) as { next?: string };
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      unwrap(await api.POST("/api/auth/sign-in/email", { body: { email, password } }));
      await qc.invalidateQueries();
      const next = search.next?.startsWith("/") && !search.next.startsWith("//") ? search.next : "/";
      window.location.assign(next);
    } catch (err) {
      setError(
        errorText(err, {
          invalid_credentials: t("auth.badCredentials"),
          unauthorized: t("auth.badCredentials"),
        }),
      );
      setBusy(false);
    }
  };

  return (
    <PublicPage title={t("auth.signInTitle")} body={t("auth.signInBody")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Notice tone="danger">{error}</Notice>}
        <Field label={t("auth.email")}>
          {(p) => (
            <Input
              {...p}
              type="email"
              autoComplete="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("auth.password")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? t("auth.signingIn") : t("auth.signIn")}
        </Button>
        <p className="text-sm text-ink-muted">{t("auth.noAccount")}</p>
      </form>
    </PublicPage>
  );
}

export function SetupPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({ token: "", name: "", email: "", password: "" });
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      unwrap(await api.POST("/api/v1/setup", { body: form }));
      unwrap(
        await api.POST("/api/auth/sign-in/email", { body: { email: form.email, password: form.password } }),
      );
      window.location.assign("/");
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        navigate({ to: "/sign-in" });
        return;
      }
      setError(errorText(err, { setup_token: t("setup.badToken"), setup_disabled: t("setup.disabled") }));
      setBusy(false);
    }
  };

  return (
    <PublicPage title={t("setup.title")} body={t("setup.body")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Notice tone="danger">{error}</Notice>}
        <Field label={t("setup.token")} hint={t("setup.tokenHint")}>
          {(p) => (
            <Input
              {...p}
              required
              autoComplete="off"
              className="font-mono"
              value={form.token}
              onChange={set("token")}
            />
          )}
        </Field>
        <Field label={t("setup.name")}>
          {(p) => <Input {...p} required autoComplete="name" value={form.name} onChange={set("name")} />}
        </Field>
        <Field label={t("setup.email")}>
          {(p) => (
            <Input
              {...p}
              type="email"
              required
              autoComplete="email"
              value={form.email}
              onChange={set("email")}
            />
          )}
        </Field>
        <Field label={t("setup.password")} hint={t("setup.passwordHint")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={form.password}
              onChange={set("password")}
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? t("setup.submitting") : t("setup.submit")}
        </Button>
      </form>
    </PublicPage>
  );
}

function tokenFromHash(): string {
  return window.location.hash.replace(/^#/, "");
}

const roleLabel: Record<string, string> = {
  member: t("users.roleMember"),
  reviewer: t("users.roleReviewer"),
  admin: t("users.roleAdmin"),
};

export function InvitePage() {
  const [token] = useState(tokenFromHash);
  const lookup = useQuery({
    queryKey: ["invite", token],
    queryFn: async () => unwrap(await api.POST("/api/v1/invites/lookup", { body: { token } })),
    enabled: token !== "",
  });
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  if (token === "" || lookup.isError) {
    return <PublicPage title={t("invite.invalidTitle")} body={t("invite.invalidBody")} />;
  }
  if (!lookup.data) {
    return (
      <PublicPage title={t("invite.title")}>
        <SkeletonRows rows={3} label={t("app.loading")} />
      </PublicPage>
    );
  }
  const invite = lookup.data;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      unwrap(await api.POST("/api/v1/invites/accept", { body: { token, name, password } }));
      unwrap(await api.POST("/api/auth/sign-in/email", { body: { email: invite.email, password } }));
      window.location.assign("/");
    } catch (err) {
      setError(errorText(err, { invite_invalid: t("invite.invalidBody") }));
      setBusy(false);
    }
  };

  return (
    <PublicPage
      title={t("invite.title")}
      body={t("invite.body", { role: roleLabel[invite.role] ?? invite.role, email: invite.email })}
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Notice tone="danger">{error}</Notice>}
        <Field label={t("invite.name")}>
          {(p) => (
            <Input
              {...p}
              required
              autoComplete="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("invite.password")} hint={t("invite.passwordHint")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? t("invite.submitting") : t("invite.submit")}
        </Button>
      </form>
    </PublicPage>
  );
}

export function ResetPage() {
  const [token] = useState(tokenFromHash);
  const [password, setPassword] = useState("");
  const [state, setState] = useState<"form" | "busy" | "done" | "invalid">(token ? "form" : "invalid");
  const [error, setError] = useState<string>();

  if (state === "invalid") {
    return <PublicPage title={t("reset.invalidTitle")} body={t("reset.invalidBody")} />;
  }
  if (state === "done") {
    return (
      <PublicPage title={t("reset.title")}>
        <Notice tone="success">{t("reset.done")}</Notice>
        <Button asChild variant="primary" className="mt-4 w-full">
          <a href="/sign-in">{t("auth.signIn")}</a>
        </Button>
      </PublicPage>
    );
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setState("busy");
    setError(undefined);
    try {
      unwrap(await api.POST("/api/v1/password-resets/accept", { body: { token, password } }));
      setState("done");
    } catch (err) {
      if (err instanceof ApiError && err.code === "reset_invalid") {
        setState("invalid");
        return;
      }
      setError(errorText(err, {}));
      setState("form");
    }
  };

  return (
    <PublicPage title={t("reset.title")} body={t("reset.body")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Notice tone="danger">{error}</Notice>}
        <Field label={t("reset.password")} hint={t("reset.passwordHint")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={state === "busy"}>
          {state === "busy" ? t("reset.submitting") : t("reset.submit")}
        </Button>
      </form>
    </PublicPage>
  );
}
