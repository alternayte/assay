import { useMutation } from "@tanstack/react-query";
import { Flag } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { EntityPicker } from "@/components/merge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { t } from "@/copy";

type Problem = "wrong_value" | "outdated" | "not_here";
type Entity = Schemas["Entity"];

/** The claim that a report is about. entity is true for an entity-valued claim. */
export type Reported = { id: number; title: string; entity: boolean };

/** Report a problem on a claim (REQ-REV-001). */
export function ReportProblem({ claim }: { claim: Reported }) {
  const [open, setOpen] = useState(false);
  const [sent, setSent] = useState(false);
  return (
    <div className="flex flex-col gap-2">
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)} className="self-start">
        <Flag aria-hidden />
        {t("report.open")}
      </Button>
      {sent && <Notice tone="success">{t("report.sent")}</Notice>}
      {open && (
        <ReportDialog
          claim={claim}
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

function ReportDialog({
  claim,
  onClose,
  onDone,
}: {
  claim: Reported;
  onClose: () => void;
  onDone: () => void;
}) {
  const [problem, setProblem] = useState<Problem>("wrong_value");
  const [reason, setReason] = useState("");
  const [text, setText] = useState("");
  const [entity, setEntity] = useState<Entity>();
  const [note, setNote] = useState("");
  const [error, setError] = useState<"reason" | "value">();
  const value = claim.entity
    ? entity
      ? { key: entity.key }
      : undefined
    : text.trim()
      ? { text: text.trim() }
      : undefined;
  const send = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/claims/{id}/problems", {
          params: { path: { id: claim.id } },
          body: {
            problem,
            reason: reason.trim(),
            value,
            source: note.trim() ? { note: note.trim() } : undefined,
          },
        }),
      ),
    onSuccess: onDone,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!reason.trim()) return setError("reason");
    if (problem === "wrong_value" && !value) return setError("value");
    setError(undefined);
    send.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("report.title")} description={t("report.body")} closeLabel={t("report.close")}>
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <p className="rounded-sm border-l-4 border-rule-strong bg-paper-sunk px-3 py-1 break-words">
            {claim.title}
          </p>
          {send.isError && (
            <Notice tone="danger">
              {send.error instanceof ApiError ? send.error.message : t("errors.network")}
            </Notice>
          )}
          <Field label={t("report.problem")}>
            {(p) => (
              <Select {...p} value={problem} onChange={(e) => setProblem(e.target.value as Problem)}>
                <option value="wrong_value">{t("report.wrong_value")}</option>
                <option value="outdated">{t("report.outdated")}</option>
                <option value="not_here">{t("report.not_here")}</option>
              </Select>
            )}
          </Field>
          <Field
            label={t("report.reason")}
            hint={t("report.reasonHint")}
            error={error === "reason" ? t("report.reasonRequired") : undefined}
          >
            {(p) => (
              <Textarea
                {...p}
                rows={3}
                maxLength={2000}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            )}
          </Field>
          {claim.entity ? (
            <div className="flex flex-col gap-1">
              <EntityPicker
                label={t("report.value")}
                hint={t("report.entityHint")}
                value={entity}
                onChange={setEntity}
              />
              {error === "value" && (
                <p className="text-sm text-danger" role="alert">
                  {t("report.valueRequired")}
                </p>
              )}
            </div>
          ) : (
            <Field
              label={t("report.value")}
              hint={t("report.valueHint")}
              error={error === "value" ? t("report.valueRequired") : undefined}
            >
              {(p) => (
                <Input {...p} maxLength={2000} value={text} onChange={(e) => setText(e.target.value)} />
              )}
            </Field>
          )}
          <Field label={t("report.note")} hint={t("report.noteHint")}>
            {(p) => <Input {...p} maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} />}
          </Field>
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("report.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={send.isPending}>
              {t("report.send")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
