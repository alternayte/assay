import { useMutation, useQuery } from "@tanstack/react-query";
import { Lightbulb } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { EntityPicker } from "@/components/merge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { t } from "@/copy";

type Entity = Schemas["Entity"];
type Predicate = Schemas["Predicate"];

/** The source item that a finding quotes. */
export type Quoted = { itemId: number; title: string };

const textKinds = ["text", "date", "any"];

/** Propose a finding with a quote from a source item (REQ-REV-005). */
export function ProposeFinding({ source }: { source: Quoted }) {
  const [open, setOpen] = useState(false);
  const [sent, setSent] = useState(false);
  return (
    <div className="flex flex-col gap-2">
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)} className="self-start">
        <Lightbulb aria-hidden />
        {t("finding.open")}
      </Button>
      {sent && <Notice tone="success">{t("finding.sent")}</Notice>}
      {open && (
        <FindingDialog
          source={source}
          onClose={() => setOpen(false)}
          onDone={() => {
            setOpen(false);
            setSent(true);
          }}
        />
      )}
    </div>
  );
}

type Missing = "subject" | "predicate" | "value" | "quote";

function FindingDialog({
  source,
  onClose,
  onDone,
}: {
  source: Quoted;
  onClose: () => void;
  onDone: () => void;
}) {
  const registry = useQuery({
    queryKey: ["predicates"],
    queryFn: async () => unwrap(await api.GET("/api/v1/predicates")),
  });
  const [subject, setSubject] = useState<Entity>();
  const [predicate, setPredicate] = useState("");
  const [kind, setKind] = useState<"entity" | "text">("entity");
  const [object, setObject] = useState<Entity>();
  const [text, setText] = useState("");
  const [quote, setQuote] = useState("");
  const [scope, setScope] = useState("");
  const [note, setNote] = useState("");
  const [missing, setMissing] = useState<Missing>();

  const options = (registry.data?.predicates ?? []).filter(
    (p) => !subject || p.subjects.includes("any") || p.subjects.includes(subject.type),
  );
  const chosen: Predicate | undefined = options.find((p) => p.name === predicate);
  const takesText = chosen ? chosen.objects.some((o) => textKinds.includes(o)) : false;
  const takesEntity = chosen ? chosen.objects.some((o) => o === "any" || !textKinds.includes(o)) : true;
  const valueKind = takesText && takesEntity ? kind : takesText ? "text" : "entity";
  const value =
    valueKind === "entity"
      ? object
        ? { key: object.key }
        : undefined
      : text.trim()
        ? { text: text.trim() }
        : undefined;

  const send = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/findings", {
          body: {
            subject: subject?.key ?? "",
            predicate,
            scope: scope.trim() || undefined,
            note: note.trim() || undefined,
            object: value ?? {},
            evidence: [{ itemId: source.itemId, quote: quote.trim() }],
          },
        }),
      ),
    onSuccess: onDone,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!subject) return setMissing("subject");
    if (!chosen) return setMissing("predicate");
    if (!value) return setMissing("value");
    if (!quote.trim()) return setMissing("quote");
    setMissing(undefined);
    send.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("finding.title")}
        description={t("finding.body")}
        closeLabel={t("finding.close")}
      >
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <p className="rounded-sm border-l-4 border-rule-strong bg-paper-sunk px-3 py-1 break-words">
            {source.title}
          </p>
          {send.isError && (
            <Notice tone="danger">
              {send.error instanceof ApiError ? send.error.message : t("errors.network")}
            </Notice>
          )}
          <div className="flex flex-col gap-1">
            <EntityPicker
              label={t("finding.subject")}
              hint={t("finding.subjectHint")}
              value={subject}
              onChange={(e) => {
                setSubject(e);
                setPredicate("");
              }}
            />
            {missing === "subject" && (
              <p className="text-sm text-danger" role="alert">
                {t("finding.subjectRequired")}
              </p>
            )}
          </div>
          <Field
            label={t("finding.predicate")}
            hint={t("finding.predicateHint")}
            error={missing === "predicate" ? t("finding.predicateRequired") : undefined}
          >
            {(p) => (
              <Select {...p} value={predicate} onChange={(e) => setPredicate(e.target.value)}>
                <option value="">{t("finding.predicatePick")}</option>
                {options.map((o) => (
                  <option key={o.name} value={o.name}>
                    {o.name.replaceAll("_", " ")}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {takesText && takesEntity && (
            <Field label={t("finding.valueKind")}>
              {(p) => (
                <Select {...p} value={kind} onChange={(e) => setKind(e.target.value as "entity" | "text")}>
                  <option value="entity">{t("finding.valueEntity")}</option>
                  <option value="text">{t("finding.valueText")}</option>
                </Select>
              )}
            </Field>
          )}
          {valueKind === "entity" ? (
            <div className="flex flex-col gap-1">
              <EntityPicker
                label={t("finding.value")}
                hint={t("finding.entityHint")}
                exclude={subject?.key}
                value={object}
                onChange={setObject}
              />
              {missing === "value" && (
                <p className="text-sm text-danger" role="alert">
                  {t("finding.valueRequired")}
                </p>
              )}
            </div>
          ) : (
            <Field
              label={t("finding.value")}
              hint={t("finding.valueHint")}
              error={missing === "value" ? t("finding.valueRequired") : undefined}
            >
              {(p) => (
                <Input {...p} maxLength={2000} value={text} onChange={(e) => setText(e.target.value)} />
              )}
            </Field>
          )}
          <Field
            label={t("finding.quote")}
            hint={t("finding.quoteHint", { title: source.title })}
            error={missing === "quote" ? t("finding.quoteRequired") : undefined}
          >
            {(p) => (
              <Textarea
                {...p}
                rows={3}
                maxLength={2000}
                value={quote}
                onChange={(e) => setQuote(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("finding.scope")} hint={t("finding.scopeHint")}>
            {(p) => <Input {...p} maxLength={100} value={scope} onChange={(e) => setScope(e.target.value)} />}
          </Field>
          <Field label={t("finding.note")} hint={t("finding.noteHint")}>
            {(p) => <Input {...p} maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} />}
          </Field>
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("finding.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={send.isPending}>
              {t("finding.send")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
