import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
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

type Group = Schemas["Group"];

function messageOf(e: unknown): string {
  if (e instanceof ApiError) return e.code === "name_taken" ? t("groups.nameTaken") : e.message;
  return t("errors.network");
}

export function GroupsPage() {
  const groups = useQuery({
    queryKey: ["groups"],
    queryFn: async () => unwrap(await api.GET("/api/v1/groups")).groups,
  });
  const [selected, setSelected] = useState<number>();
  const current = groups.data?.find((g) => g.id === selected) ?? groups.data?.[0];

  return (
    <div className="flex flex-col gap-6">
      <Sheet title={t("groups.title")} description={t("groups.body")} actions={<NameDialog mode="create" />}>
        {groups.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={3} />
        ) : groups.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : (
          <>
            <ul className="flex flex-col">
              {groups.data.map((g) => (
                <li key={g.id} className="border-b border-rule last:border-b-0">
                  <button
                    type="button"
                    onClick={() => setSelected(g.id)}
                    aria-current={current?.id === g.id ? "true" : undefined}
                    className="flex w-full items-center justify-between gap-3 px-2 py-2 text-left transition-ink hover:bg-paper-sunk aria-[current=true]:bg-accent-wash"
                  >
                    <span className="font-medium">
                      {g.name}
                      {g.builtin && (
                        <Badge className="ml-2" tone="neutral">
                          {t("groups.builtin")}
                        </Badge>
                      )}
                    </span>
                    <span className="font-mono text-xs text-ink-muted">
                      {t("groups.memberCount", { count: g.memberCount })}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            {groups.data.length === 1 && (
              <Empty action={<NameDialog mode="create" />}>{t("groups.empty")}</Empty>
            )}
          </>
        )}
      </Sheet>
      {current && <MembersSheet group={current} />}
    </div>
  );
}

function NameDialog({ mode, group }: { mode: "create" | "rename"; group?: Group }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(group?.name ?? "");
  const [error, setError] = useState<string>();
  const save = useMutation({
    mutationFn: async () =>
      mode === "create"
        ? unwrap(await api.POST("/api/v1/groups", { body: { name } }))
        : unwrap(
            await api.PATCH("/api/v1/groups/{id}", {
              params: { path: { id: group?.id ?? 0 } },
              body: { name },
            }),
          ),
    onSuccess: () => {
      setOpen(false);
      setName(mode === "create" ? "" : name);
      qc.invalidateQueries({ queryKey: ["groups"] });
    },
    onError: (e) => setError(messageOf(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };
  const title = mode === "create" ? t("groups.create") : t("groups.rename");
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button
        variant={mode === "create" ? "primary" : "secondary"}
        size={mode === "create" ? "md" : "sm"}
        onClick={() => setOpen(true)}
      >
        {mode === "create" && <Plus aria-hidden />}
        {title}
      </Button>
      <DialogContent title={title} closeLabel={t("groups.cancel")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {error && <Notice tone="danger">{error}</Notice>}
          <Field label={t("groups.name")}>
            {(p) => (
              <Input {...p} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
            )}
          </Field>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("groups.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function MembersSheet({ group }: { group: Group }) {
  const qc = useQueryClient();
  const members = useQuery({
    queryKey: ["groups", group.id, "members"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/groups/{id}/members", { params: { path: { id: group.id } } })).members,
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: async () => unwrap(await api.GET("/api/v1/users")).users,
    enabled: !group.builtin,
  });
  const [pick, setPick] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const refresh = () => qc.invalidateQueries({ queryKey: ["groups"] });

  const add = useMutation({
    mutationFn: async (userId: string) =>
      unwrap(
        await api.PUT("/api/v1/groups/{id}/members/{userId}", { params: { path: { id: group.id, userId } } }),
      ),
    onSuccess: () => {
      setPick("");
      refresh();
    },
  });
  const remove = useMutation({
    mutationFn: async (userId: string) =>
      unwrap(
        await api.DELETE("/api/v1/groups/{id}/members/{userId}", {
          params: { path: { id: group.id, userId } },
        }),
      ),
    onSuccess: refresh,
  });
  const del = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/api/v1/groups/{id}", { params: { path: { id: group.id } } })),
    onSuccess: () => {
      setConfirmDelete(false);
      refresh();
    },
  });

  const memberIds = new Set(members.data?.map((m) => m.id));
  const candidates = users.data?.filter((u) => u.status === "active" && !memberIds.has(u.id)) ?? [];

  return (
    <Sheet
      title={`${t("groups.members")}: ${group.name}`}
      description={group.builtin ? t("groups.everyoneHelp") : undefined}
      actions={
        !group.builtin && (
          <>
            <NameDialog key={group.id} mode="rename" group={group} />
            <Button size="sm" variant="danger" onClick={() => setConfirmDelete(true)}>
              {t("groups.delete")}
            </Button>
          </>
        )
      }
    >
      {!group.builtin && (
        <form
          className="mb-4 flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (pick) add.mutate(pick);
          }}
        >
          <Field label={t("groups.addMember")} className="min-w-64 flex-1">
            {(p) => (
              <Select {...p} value={pick} onChange={(e) => setPick(e.target.value)}>
                <option value="">{t("groups.chooseUser")}</option>
                {candidates.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name} ({u.email})
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Button type="submit" disabled={!pick || add.isPending}>
            {t("groups.addMember")}
          </Button>
        </form>
      )}
      {members.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : members.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : members.data.length === 0 ? (
        <Empty>{t("groups.noMembers")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("users.colUser")}</Th>
              <Th>{t("users.colStatus")}</Th>
              {!group.builtin && (
                <Th>
                  <span className="sr-only">{t("users.colActions")}</span>
                </Th>
              )}
            </tr>
          </thead>
          <tbody>
            {members.data.map((m) => (
              <tr key={m.id}>
                <Td>
                  <div className="font-medium">{m.name}</div>
                  <div className="font-mono text-xs text-ink-muted">{m.email}</div>
                </Td>
                <Td>
                  {m.status === "active" ? (
                    <Badge tone="supported">{t("users.active")}</Badge>
                  ) : (
                    <Badge tone="stale">{t("users.deactivated")}</Badge>
                  )}
                </Td>
                {!group.builtin && (
                  <Td className="text-right">
                    <Button size="sm" variant="ghost" onClick={() => remove.mutate(m.id)}>
                      {t("groups.remove")}
                    </Button>
                  </Td>
                )}
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent title={t("groups.delete")} closeLabel={t("groups.cancel")}>
          <p>{t("groups.confirmDelete", { name: group.name })}</p>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setConfirmDelete(false)}>{t("groups.cancel")}</Button>
            <Button variant="danger" onClick={() => del.mutate()}>
              {t("groups.delete")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Sheet>
  );
}
