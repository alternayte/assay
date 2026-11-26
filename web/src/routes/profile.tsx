import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { ApiError, api, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";
import { formatTime } from "@/lib/format";
import { useMe } from "@/lib/session";

type SessionRow = { id: string; createdAt: string; lastSeenAt?: string };

export function ProfilePage() {
  const me = useMe();
  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-3xl font-semibold tracking-tight">{t("profile.title")}</h1>
      <Sheet title={t("profile.details")}>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2">
          <dt className="label pt-1">{t("profile.name")}</dt>
          <dd>{me.name}</dd>
          <dt className="label pt-1">{t("profile.email")}</dt>
          <dd className="font-mono text-sm leading-6">{me.email}</dd>
          <dt className="label pt-1">{t("profile.role")}</dt>
          <dd>
            <Badge tone="accent">{me.role}</Badge>
          </dd>
        </dl>
      </Sheet>
      <PasswordSheet />
      <SessionsSheet />
      <AppsSheet />
    </div>
  );
}

/** The copy key of each scope of an OAuth grant. */
const scopeLabel: Record<string, "tokens.read" | "tokens.propose"> = {
  read: "tokens.read",
  propose: "tokens.propose",
};

/** Connected apps: the MCP clients that the user approved, with Disconnect. */
function AppsSheet() {
  const qc = useQueryClient();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  const apps = useQuery({
    queryKey: ["connected-apps"],
    queryFn: async () => unwrap(await api.GET("/api/v1/connected-apps")).apps,
  });
  const disconnect = useMutation({
    mutationFn: async (clientID: string) =>
      unwrap(await api.DELETE("/api/auth/oauth2/consents/{clientID}", { params: { path: { clientID } } })),
    onSuccess: () => {
      setMsg({ tone: "success", text: t("profile.disconnected") });
      qc.invalidateQueries({ queryKey: ["connected-apps"] });
    },
    onError: () => setMsg({ tone: "danger", text: t("errors.generic") }),
  });
  return (
    <Sheet title={t("profile.appsTitle")} description={t("profile.appsBody")}>
      <div className="flex flex-col gap-3">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        {apps.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={2} />
        ) : apps.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : apps.data.length === 0 ? (
          <p className="text-base text-ink-muted">{t("profile.appsEmpty")}</p>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t("profile.colApp")}</Th>
                <Th>{t("profile.colHost")}</Th>
                <Th>{t("profile.colScopes")}</Th>
                <Th>{t("profile.colConnected")}</Th>
                <Th>
                  <span className="sr-only">{t("users.colActions")}</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {apps.data.map((a) => (
                <tr key={a.clientId} data-testid="connected-app">
                  <Td>{a.name}</Td>
                  <Td className="font-mono text-sm break-all">{a.host}</Td>
                  <Td>
                    {a.scopes
                      .flatMap((s) => {
                        const label = scopeLabel[s];
                        return label ? [t(label)] : [];
                      })
                      .join(", ")}
                  </Td>
                  <Td className="font-mono text-sm">{formatTime(a.connectedAt)}</Td>
                  <Td className="text-right">
                    <Button
                      size="sm"
                      variant="danger"
                      aria-label={t("profile.disconnectName", { name: a.name })}
                      disabled={disconnect.isPending}
                      onClick={() => {
                        setMsg(undefined);
                        disconnect.mutate(a.clientId);
                      }}
                    >
                      {t("profile.disconnect")}
                    </Button>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </Sheet>
  );
}

function PasswordSheet() {
  const qc = useQueryClient();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  const change = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/auth/password/change", {
          body: { currentPassword: current, newPassword: next, revokeOtherSessions: true },
        }),
      ),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setMsg({ tone: "success", text: t("profile.changed") });
      qc.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (e) =>
      setMsg({
        tone: "danger",
        text:
          e instanceof ApiError && e.status < 500 && e.status !== 422
            ? t("profile.wrongCurrent")
            : t("errors.generic"),
      }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    change.mutate();
  };
  return (
    <Sheet title={t("profile.passwordTitle")}>
      <form onSubmit={submit} className="flex max-w-md flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <Field label={t("profile.current")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              required
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("profile.next")} hint={t("reset.passwordHint")}>
          {(p) => (
            <Input
              {...p}
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          )}
        </Field>
        <div>
          <Button type="submit" variant="primary" disabled={change.isPending}>
            {t("profile.change")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

function SessionsSheet() {
  const qc = useQueryClient();
  const sessions = useQuery({
    queryKey: ["sessions"],
    queryFn: async () => {
      const list = unwrap(await api.GET("/api/auth/sessions")) as unknown as { sessions: SessionRow[] };
      const current = unwrap(await api.GET("/api/auth/session")) as unknown as { session?: { id?: string } };
      return { list: list.sessions, current: current.session?.id };
    },
  });
  const revoke = useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/api/auth/sessions/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
  const revokeOthers = useMutation({
    mutationFn: async () => unwrap(await api.POST("/api/auth/sessions/revoke-all", { body: {} })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });

  return (
    <Sheet
      title={t("profile.sessionsTitle")}
      description={t("profile.sessionsBody")}
      actions={
        <Button size="sm" onClick={() => revokeOthers.mutate()} disabled={revokeOthers.isPending}>
          {t("profile.revokeOthers")}
        </Button>
      }
    >
      {sessions.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={2} />
      ) : sessions.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("profile.created")}</Th>
              <Th>{t("profile.lastSeen")}</Th>
              <Th>
                <span className="sr-only">{t("users.colActions")}</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {sessions.data.list.map((s) => (
              <tr key={s.id}>
                <Td>
                  {formatTime(s.createdAt)}
                  {s.id === sessions.data.current && (
                    <Badge tone="accent" className="ml-2">
                      {t("profile.thisSession")}
                    </Badge>
                  )}
                </Td>
                <Td>{s.lastSeenAt ? formatTime(s.lastSeenAt) : ""}</Td>
                <Td className="text-right">
                  {s.id !== sessions.data.current && (
                    <Button size="sm" variant="ghost" onClick={() => revoke.mutate(s.id)}>
                      {t("profile.revoke")}
                    </Button>
                  )}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Sheet>
  );
}
