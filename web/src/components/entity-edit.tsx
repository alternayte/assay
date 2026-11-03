import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Pencil, Trash2, X } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { type CopyKey, t } from "@/copy";
import { atLeast, useMe } from "@/lib/session";

type Detail = Schemas["Detail"];

const errorText = (e: unknown) => (e instanceof ApiError ? e.message : t("errors.network"));
const typeLabel = (k: string) => t(`entityTypes.${k}` as CopyKey);

/** Edit and delete of an entity from its page. A reviewer's change applies at once; a member's change becomes a card. */
export function EditActions({ entity, onChange }: { entity: Detail; onChange: () => void }) {
  const reviewer = atLeast(useMe(), "reviewer");
  const [open, setOpen] = useState<"edit" | "delete">();
  const [msg, setMsg] = useState<string>();
  const done = () => {
    setMsg(reviewer ? t("edit.saved") : t("edit.proposed"));
    onChange();
  };
  if (entity.record) {
    return (
      <Link
        to="/registry/$key"
        params={{ key: entity.key }}
        className="text-sm text-accent underline-offset-2 hover:underline"
      >
        {t("edit.record")}
      </Link>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => setOpen("edit")}>
          <Pencil aria-hidden />
          {t("edit.open")}
        </Button>
        {entity.deletable && (
          <Button size="sm" variant="danger" onClick={() => setOpen("delete")}>
            <Trash2 aria-hidden />
            {t("edit.delete")}
          </Button>
        )}
      </div>
      {msg && <Notice tone="success">{msg}</Notice>}
      {open === "edit" && (
        <EditDialog entity={entity} reviewer={reviewer} onClose={() => setOpen(undefined)} onDone={done} />
      )}
      {open === "delete" && (
        <DeleteDialog entity={entity} reviewer={reviewer} onClose={() => setOpen(undefined)} onDone={done} />
      )}
    </div>
  );
}

function EditDialog({
  entity,
  reviewer,
  onClose,
  onDone,
}: {
  entity: Detail;
  reviewer: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const key = { params: { path: { key: entity.key } } };
  const [name, setName] = useState(entity.name);
  const [alias, setAlias] = useState("");
  const [type, setType] = useState(entity.type);
  const save = useMutation({
    mutationFn: async (change: () => Promise<unknown>) => change(),
    onSuccess: onDone,
  });
  const rename = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(async () =>
      unwrap(await api.PUT("/api/v1/entities/{key}/name", { ...key, body: { name: name.trim() } })),
    );
  };
  const add = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(async () => {
      unwrap(await api.POST("/api/v1/entities/{key}/aliases", { ...key, body: { alias: alias.trim() } }));
      setAlias("");
    });
  };
  const remove = (id: number) =>
    save.mutate(async () =>
      unwrap(
        await api.DELETE("/api/v1/entities/{key}/aliases/{aliasId}", {
          params: { path: { key: entity.key, aliasId: id } },
        }),
      ),
    );
  const retype = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(async () => unwrap(await api.PUT("/api/v1/entities/{key}/type", { ...key, body: { type } })));
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("edit.title", { name: entity.name })}
        description={reviewer ? t("edit.reviewerBody") : t("edit.memberBody")}
        closeLabel={t("edit.close")}
      >
        <div className="flex flex-col gap-5">
          {save.isError && <Notice tone="danger">{errorText(save.error)}</Notice>}
          {save.isSuccess && (
            <Notice tone="success">{reviewer ? t("edit.saved") : t("edit.proposed")}</Notice>
          )}
          <form onSubmit={rename} className="flex flex-col gap-2">
            <Field label={t("edit.name")}>
              {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            <div>
              <Button type="submit" disabled={save.isPending || !name.trim() || name.trim() === entity.name}>
                {t("edit.saveName")}
              </Button>
            </div>
          </form>
          <div className="flex flex-col gap-2">
            <span className="label">{t("edit.aliases")}</span>
            <ul className="flex flex-col divide-y divide-rule" data-testid="aliases">
              {entity.aliasRefs.map((a) => (
                <li key={a.id} className="flex items-center justify-between gap-3 py-1.5 text-sm">
                  <span className="break-words">{a.alias}</span>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("edit.removeAlias", { alias: a.alias })}
                    disabled={save.isPending}
                    onClick={() => remove(a.id)}
                  >
                    <X aria-hidden />
                  </Button>
                </li>
              ))}
            </ul>
            <form onSubmit={add} className="flex flex-wrap items-end gap-2">
              <Field label={t("edit.addAlias")} className="min-w-0 flex-1">
                {(p) => (
                  <Input {...p} maxLength={200} value={alias} onChange={(e) => setAlias(e.target.value)} />
                )}
              </Field>
              <Button type="submit" disabled={save.isPending || !alias.trim()}>
                {t("edit.add")}
              </Button>
            </form>
          </div>
          {entity.allowedTypes.length > 0 && (
            <form onSubmit={retype} className="flex flex-wrap items-end gap-2">
              <Field label={t("edit.type")} className="min-w-0 flex-1">
                {(p) => (
                  <Select {...p} value={type} onChange={(e) => setType(e.target.value)}>
                    {[...new Set([entity.type, ...entity.allowedTypes])].map((ty) => (
                      <option key={ty} value={ty}>
                        {typeLabel(ty)}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Button type="submit" disabled={save.isPending || type === entity.type}>
                {t("edit.saveType")}
              </Button>
            </form>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDialog({
  entity,
  reviewer,
  onClose,
  onDone,
}: {
  entity: Detail;
  reviewer: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const navigate = useNavigate();
  const del = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/api/v1/entities/{key}", { params: { path: { key: entity.key } } })),
    onSuccess: () => {
      if (reviewer) navigate({ to: "/explore" });
      else {
        onClose();
        onDone();
      }
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("edit.deleteTitle", { name: entity.name })}
        description={reviewer ? t("edit.deleteBody") : t("edit.deleteMemberBody")}
        closeLabel={t("edit.close")}
      >
        <div className="flex flex-col gap-4">
          {del.isError && <Notice tone="danger">{errorText(del.error)}</Notice>}
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("edit.cancel")}</Button>
            <Button variant="danger" disabled={del.isPending} onClick={() => del.mutate()}>
              {reviewer ? t("edit.deleteConfirm") : t("edit.propose")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
