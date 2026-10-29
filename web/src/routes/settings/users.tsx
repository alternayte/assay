import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound, UserPlus } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";
import { formatTime } from "@/lib/format";
import { useMe } from "@/lib/session";

type Role = Schemas["UserRecord"]["role"];
const roles: { value: Role; label: string }[] = [
  { value: "member", label: t("users.roleMember") },
  { value: "reviewer", label: t("users.roleReviewer") },
  { value: "admin", label: t("users.roleAdmin") },
];

function messageOf(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "last_admin") return t("users.lastAdmin");
    if (e.code === "user_exists") return t("users.userExists");
    if (e.status === 403) return t("errors.forbidden");
    return e.message;
  }
  return t("errors.network");
}

export function CopyLink({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-col gap-2">
      <Input
        readOnly
        value={url}
        className="font-mono text-sm"
        aria-label={t("users.linkTitle")}
        onFocus={(e) => e.target.select()}
      />
      <div>
        <Button
          onClick={async () => {
            await navigator.clipboard.writeText(url);
            setCopied(true);
          }}
        >
          <Copy aria-hidden />
          {copied ? t("users.copied") : t("users.copy")}
        </Button>
      </div>
    </div>
  );
}

export function UsersPage() {
  const me = useMe();
  const qc = useQueryClient();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: async () => unwrap(await api.GET("/api/v1/users")).users,
  });
  const [error, setError] = useState<string>();
  const [resetUrl, setResetUrl] = useState<string>();
  const [confirm, setConfirm] = useState<Schemas["UserRecord"]>();
  const [inviting, setInviting] = useState(false);
  const inviteButton = (
    <Button variant="primary" onClick={() => setInviting(true)}>
      <UserPlus aria-hidden />
      {t("users.invite")}
    </Button>
  );

  const onError = (e: unknown) => setError(messageOf(e));
  const refresh = () => qc.invalidateQueries({ queryKey: ["users"] });

  const setRole = useMutation({
    mutationFn: async (v: { id: string; role: Role }) =>
      unwrap(
        await api.PUT("/api/v1/users/{id}/role", { params: { path: { id: v.id } }, body: { role: v.role } }),
      ),
    onSuccess: refresh,
    onError,
  });
  const setActive = useMutation({
    mutationFn: async (v: { id: string; active: boolean }) =>
      unwrap(
        v.active
          ? await api.POST("/api/v1/users/{id}/reactivate", { params: { path: { id: v.id } } })
          : await api.POST("/api/v1/users/{id}/deactivate", { params: { path: { id: v.id } } }),
      ),
    onSuccess: () => {
      setConfirm(undefined);
      refresh();
    },
    onError: (e) => {
      setConfirm(undefined);
      onError(e);
    },
  });
  const reset = useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/api/v1/users/{id}/reset-link", { params: { path: { id } } })),
    onSuccess: (r) => setResetUrl(r.url),
    onError,
  });

  return (
    <div className="flex flex-col gap-6">
      <Sheet title={t("users.title")} description={t("users.body")} actions={inviteButton}>
        {error && (
          <Notice tone="danger" className="mb-3">
            {error}
          </Notice>
        )}
        {users.isPending ? (
          <SkeletonRows label={t("app.loading")} />
        ) : users.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t("users.colUser")}</Th>
                <Th>{t("users.colRole")}</Th>
                <Th>{t("users.colStatus")}</Th>
                <Th className="text-right">{t("users.colActions")}</Th>
              </tr>
            </thead>
            <tbody>
              {users.data.map((u) => (
                <tr key={u.id}>
                  <Td>
                    <div className="font-medium">
                      {u.name}
                      {u.id === me.id && (
                        <Badge className="ml-2" tone="accent">
                          {t("users.you")}
                        </Badge>
                      )}
                    </div>
                    <div className="font-mono text-xs text-ink-muted">{u.email}</div>
                  </Td>
                  <Td>
                    <Select
                      aria-label={`${t("users.colRole")}: ${u.email}`}
                      value={u.role}
                      className="h-7 w-36 text-sm"
                      onChange={(e) => {
                        setError(undefined);
                        setRole.mutate({ id: u.id, role: e.target.value as Role });
                      }}
                    >
                      {roles.map((r) => (
                        <option key={r.value} value={r.value}>
                          {r.label}
                        </option>
                      ))}
                    </Select>
                  </Td>
                  <Td>
                    {u.status === "active" ? (
                      <Badge tone="supported">{t("users.active")}</Badge>
                    ) : (
                      <Badge tone="stale">{t("users.deactivated")}</Badge>
                    )}
                  </Td>
                  <Td className="text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => reset.mutate(u.id)}>
                      <KeyRound aria-hidden />
                      {t("users.resetLink")}
                    </Button>
                    {u.id !== me.id &&
                      (u.status === "active" ? (
                        <Button size="sm" variant="ghost" onClick={() => setConfirm(u)}>
                          {t("users.deactivate")}
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setActive.mutate({ id: u.id, active: true })}
                        >
                          {t("users.reactivate")}
                        </Button>
                      ))}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        <p className="mt-3 text-sm text-ink-muted">{t("users.roleHelp")}</p>
      </Sheet>
      <InvitesSheet inviteButton={inviteButton} />
      <InviteDialog open={inviting} onOpenChange={setInviting} />

      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(undefined)}>
        <DialogContent title={t("users.deactivate")} closeLabel={t("users.cancel")}>
          <p>{t("users.confirmDeactivate", { email: confirm?.email ?? "" })}</p>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setConfirm(undefined)}>{t("users.cancel")}</Button>
            <Button
              variant="danger"
              onClick={() => confirm && setActive.mutate({ id: confirm.id, active: false })}
            >
              {t("users.deactivate")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog open={!!resetUrl} onOpenChange={(o) => !o && setResetUrl(undefined)}>
        <DialogContent
          title={t("users.resetTitle")}
          description={t("users.resetBody")}
          closeLabel={t("users.done")}
        >
          {resetUrl && <CopyLink url={resetUrl} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function InviteDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [result, setResult] = useState<{ url: string; emailed: boolean }>();
  const [error, setError] = useState<string>();
  const create = useMutation({
    mutationFn: async () => unwrap(await api.POST("/api/v1/invites", { body: { email, role } })),
    onSuccess: (r) => {
      setResult({ url: r.url, emailed: r.emailed });
      qc.invalidateQueries({ queryKey: ["invites"] });
    },
    onError: (e) => setError(messageOf(e)),
  });
  const close = (o: boolean) => {
    onOpenChange(o);
    if (!o) {
      setEmail("");
      setRole("member");
      setResult(undefined);
      setError(undefined);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    create.mutate();
  };
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent
        title={result ? t("users.linkTitle") : t("users.inviteTitle")}
        description={result ? t("users.linkBody") : t("users.inviteBody")}
        closeLabel={t("users.done")}
      >
        {result ? (
          <div className="flex flex-col gap-3">
            <CopyLink url={result.url} />
            {result.emailed && <Notice tone="success">{t("users.emailed")}</Notice>}
          </div>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            {error && <Notice tone="danger">{error}</Notice>}
            <Field label={t("users.email")}>
              {(p) => (
                <Input
                  {...p}
                  type="email"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              )}
            </Field>
            <Field label={t("users.role")} hint={t("users.roleHelp")}>
              {(p) => (
                <Select {...p} value={role} onChange={(e) => setRole(e.target.value as Role)}>
                  {roles.map((r) => (
                    <option key={r.value} value={r.value}>
                      {r.label}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <div className="flex justify-end">
              <Button type="submit" variant="primary" disabled={create.isPending}>
                {t("users.create")}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function InvitesSheet({ inviteButton }: { inviteButton: React.ReactNode }) {
  const qc = useQueryClient();
  const invites = useQuery({
    queryKey: ["invites"],
    queryFn: async () => unwrap(await api.GET("/api/v1/invites")).invites,
  });
  const revoke = useMutation({
    mutationFn: async (id: number) =>
      unwrap(await api.DELETE("/api/v1/invites/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["invites"] }),
  });
  return (
    <Sheet title={t("users.openInvites")}>
      {invites.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={2} />
      ) : invites.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : invites.data.length === 0 ? (
        <Empty action={inviteButton}>{t("users.noInvites")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("users.email")}</Th>
              <Th>{t("users.role")}</Th>
              <Th>{t("users.expires")}</Th>
              <Th className="text-right">{t("users.colActions")}</Th>
            </tr>
          </thead>
          <tbody>
            {invites.data.map((i) => (
              <tr key={i.id}>
                <Td className="font-mono text-sm">{i.email}</Td>
                <Td>{roles.find((r) => r.value === i.role)?.label ?? i.role}</Td>
                <Td>{formatTime(i.expiresAt)}</Td>
                <Td className="text-right">
                  <Button size="sm" variant="ghost" onClick={() => revoke.mutate(i.id)}>
                    {t("users.revoke")}
                  </Button>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Sheet>
  );
}
