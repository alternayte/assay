import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";

type Domain = Schemas["Domain"];

function messageOf(e: unknown): string {
  if (e instanceof ApiError) return e.code === "name_taken" ? t("domains.nameTaken") : e.message;
  return t("errors.network");
}

export function DomainsPage() {
  const domains = useQuery({
    queryKey: ["domains"],
    queryFn: async () => unwrap(await api.GET("/api/v1/domains")).domains,
  });
  return (
    <Sheet title={t("domains.title")} description={t("domains.body")} actions={<DomainDialog />}>
      {domains.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : domains.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : domains.data.length === 0 ? (
        <Empty action={<DomainDialog />}>{t("domains.empty")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("domains.name")}</Th>
              <Th>{t("domains.description")}</Th>
              <Th>
                <span className="sr-only">{t("domains.rename")}</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {domains.data.map((d) => (
              <tr key={d.id}>
                <Td className="font-medium">{d.name}</Td>
                <Td className="text-ink-muted">{d.description}</Td>
                <Td className="text-right">
                  <div className="flex justify-end gap-2">
                    <DomainDialog domain={d} />
                    <DeleteDomain domain={d} />
                  </div>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Sheet>
  );
}

function DomainDialog({ domain }: { domain?: Domain }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(domain?.name ?? "");
  const [description, setDescription] = useState(domain?.description ?? "");
  const [error, setError] = useState<string>();
  const save = useMutation({
    mutationFn: async () =>
      domain
        ? unwrap(
            await api.PUT("/api/v1/domains/{id}", {
              params: { path: { id: domain.id } },
              body: { name, description },
            }),
          )
        : unwrap(await api.POST("/api/v1/domains", { body: { name, description } })),
    onSuccess: () => {
      setOpen(false);
      if (!domain) {
        setName("");
        setDescription("");
      }
      qc.invalidateQueries({ queryKey: ["domains"] });
    },
    onError: (e) => setError(messageOf(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };
  const title = domain ? t("domains.rename") : t("domains.create");
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button
        variant={domain ? "secondary" : "primary"}
        size={domain ? "sm" : "md"}
        onClick={() => setOpen(true)}
      >
        {!domain && <Plus aria-hidden />}
        {title}
      </Button>
      <DialogContent title={title} closeLabel={t("domains.cancel")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {error && <Notice tone="danger">{error}</Notice>}
          <Field label={t("domains.name")}>
            {(p) => (
              <Input {...p} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
            )}
          </Field>
          <Field label={t("domains.description")}>
            {(p) => (
              <Input
                {...p}
                maxLength={1000}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            )}
          </Field>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("domains.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDomain({ domain }: { domain: Domain }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const del = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/api/v1/domains/{id}", { params: { path: { id: domain.id } } })),
    onSuccess: () => {
      setOpen(false);
      qc.invalidateQueries({ queryKey: ["domains"] });
    },
  });
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button size="sm" variant="danger" onClick={() => setOpen(true)}>
        {t("domains.delete")}
      </Button>
      <DialogContent title={t("domains.delete")} closeLabel={t("domains.cancel")}>
        <p>{t("domains.confirmDelete", { name: domain.name })}</p>
        <div className="flex justify-end gap-2">
          <Button onClick={() => setOpen(false)}>{t("domains.cancel")}</Button>
          <Button variant="danger" onClick={() => del.mutate()}>
            {t("domains.delete")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
