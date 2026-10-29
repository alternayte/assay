import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { type CopyKey, t } from "@/copy";
import { useSaveSetting, useSetting } from "@/routes/settings";

type KindCalibration = Schemas["KindCalibration"];
type Kind = "same_entity" | "mention_link" | "describes" | "equivalent" | "still_supports" | "term_worthy";
const bands = ["high", "medium", "low"] as const;

/** Decision bands, measured precision and auto-apply per kind, and the term thresholds. */
export function DecisionsPage() {
  const cal = useQuery({
    queryKey: ["calibration"],
    queryFn: async () => unwrap(await api.GET("/api/v1/calibration")).kinds,
  });
  return (
    <div className="flex flex-col gap-6">
      <Sheet title={t("decisions.title")} description={t("decisions.body")}>
        {cal.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={6} />
        ) : cal.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t("decisions.kind")}</Th>
                <Th className="text-right">{t("decisions.reviewed")}</Th>
                <Th>{t("decisions.calibration")}</Th>
                {bands.map((b) => (
                  <Th key={b} className="text-right">
                    {t(`decisions.band.${b}` as CopyKey)}
                  </Th>
                ))}
                <Th>{t("decisions.autoApply")}</Th>
              </tr>
            </thead>
            <tbody>
              {cal.data.map((k) => (
                <KindRow key={k.kind} k={k} />
              ))}
            </tbody>
          </Table>
        )}
      </Sheet>
      <TermThresholds />
    </div>
  );
}

function KindRow({ k }: { k: KindCalibration }) {
  const qc = useQueryClient();
  const [error, setError] = useState<string>();
  const toggle = useMutation({
    mutationFn: async (on: boolean) =>
      unwrap(
        await api.PUT("/api/v1/calibration/{kind}", {
          params: { path: { kind: k.kind as Kind } },
          body: { autoApply: on },
        }),
      ),
    onSuccess: () => {
      setError(undefined);
      qc.invalidateQueries({ queryKey: ["calibration"] });
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : t("errors.network")),
  });
  return (
    <tr data-testid={`kind-${k.kind}`}>
      <Td className="font-mono text-sm">{k.kind}</Td>
      <Td className="text-right font-mono">{k.reviewed}</Td>
      <Td>
        {k.calibrated ? (
          <Badge tone="supported">{t("decisions.calibrated")}</Badge>
        ) : (
          <Badge>{t("decisions.notCalibrated")}</Badge>
        )}
      </Td>
      {bands.map((b) => {
        const p = k.precision?.[b];
        return (
          <Td key={b} className="text-right font-mono text-sm">
            {p ? t("decisions.precision", { value: Math.round(p.value * 100), n: p.reviewed }) : "—"}
          </Td>
        );
      })}
      <Td>
        <div className="flex flex-col items-start gap-1">
          <Button
            size="sm"
            variant={k.autoApply ? "primary" : "secondary"}
            aria-pressed={k.autoApply}
            disabled={toggle.isPending || (!k.autoApply && !k.canAutoApply)}
            onClick={() => toggle.mutate(!k.autoApply)}
          >
            {k.autoApply ? t("decisions.on") : t("decisions.off")}
          </Button>
          {!k.autoApply && !k.canAutoApply && (
            <span className="text-xs text-ink-faint">{t("decisions.notEarned")}</span>
          )}
          {error && <span className="text-xs text-danger">{error}</span>}
        </div>
      </Td>
    </tr>
  );
}

type Rules = { minItems: number; minConnections: number };

function TermThresholds() {
  const setting = useSetting("terms");
  const [form, setForm] = useState<Rules>();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (setting.data) setForm(setting.data.value as Rules);
  }, [setting.data]);
  const save = useSaveSetting(
    "terms",
    (s) => setMsg({ tone: "success", text: t("decisions.termsSaved", { revision: s.revision }) }),
    (text) => setMsg({ tone: "danger", text }),
  );
  if (setting.isPending || !form) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || !setting.data) return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    save.mutate({ value: form, revision });
  };
  return (
    <Sheet title={t("decisions.termsTitle")} description={t("decisions.termsBody")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("decisions.minItems")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={2}
                value={form.minItems}
                onChange={(e) => setForm({ ...form, minItems: Number(e.target.value) })}
              />
            )}
          </Field>
          <Field label={t("decisions.minConnections")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={1}
                value={form.minConnections}
                onChange={(e) => setForm({ ...form, minConnections: Number(e.target.value) })}
              />
            )}
          </Field>
        </div>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("decisions.save")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}
