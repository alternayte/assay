import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";
import { formatTime } from "@/lib/format";

export function DiagnosticsPage() {
  const run = useMutation({ mutationFn: async () => unwrap(await api.POST("/api/v1/diagnostics")) });
  return (
    <div className="flex flex-col gap-6">
      <Sheet
        title={t("diagnostics.title")}
        description={t("diagnostics.body")}
        actions={
          <Button variant="primary" onClick={() => run.mutate()} disabled={run.isPending}>
            {run.isPending ? t("diagnostics.running") : t("diagnostics.run")}
          </Button>
        }
      >
        {run.isPending ? (
          <SkeletonRows label={t("diagnostics.running")} rows={6} />
        ) : run.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : !run.data ? (
          <Empty>{t("diagnostics.notRun")}</Empty>
        ) : (
          <div className="flex flex-col gap-3">
            <Notice tone={run.data.ok ? "success" : "danger"}>
              {run.data.ok ? t("diagnostics.allPass") : t("diagnostics.someFail")}
            </Notice>
            <Table>
              <thead>
                <tr>
                  <Th>{t("models.colStatus")}</Th>
                  <Th>{t("diagnostics.colCheck")}</Th>
                  <Th>{t("diagnostics.colResult")}</Th>
                </tr>
              </thead>
              <tbody>
                {run.data.checks.map((c) => (
                  <tr key={c.name}>
                    <Td>
                      {c.ok ? (
                        <Badge tone="supported">{t("diagnostics.pass")}</Badge>
                      ) : (
                        <Badge tone="danger">{t("diagnostics.fail")}</Badge>
                      )}
                    </Td>
                    <Td className="font-medium">{c.name}</Td>
                    <Td className="text-sm break-words text-ink-muted">{c.reason}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        )}
      </Sheet>
      <Sheet
        title={t("diagnostics.jobsTitle")}
        actions={
          <Button asChild>
            <a href="/admin/jobs/">{t("diagnostics.jobsOpen")}</a>
          </Button>
        }
      >
        <p>{t("diagnostics.jobsBody")}</p>
      </Sheet>
      <QuestionsToImprove />
    </div>
  );
}

/** Questions with no evidence or a not-helpful mark, grouped by the thing they name. No row names the asker. */
function QuestionsToImprove() {
  const list = useQuery({
    queryKey: ["diagnostics", "questions"],
    queryFn: async () => unwrap(await api.GET("/api/v1/diagnostics/questions")).groups,
  });
  return (
    <Sheet title={t("diagnostics.improveTitle")} description={t("diagnostics.improveBody")}>
      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={4} />
      ) : list.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : list.data.length === 0 ? (
        <Empty>{t("diagnostics.improveEmpty")}</Empty>
      ) : (
        <div className="flex flex-col gap-6">
          {list.data.map((g) => (
            <section key={g.entityKey || "none"} aria-labelledby={`improve-${g.entityKey || "none"}`}>
              <h3
                id={`improve-${g.entityKey || "none"}`}
                className="mb-2 flex flex-wrap items-baseline gap-2 text-base font-semibold"
              >
                {g.entityKey ? g.entityName : t("diagnostics.noName")}
                <span className="font-mono text-xs font-normal text-ink-muted">
                  {t("diagnostics.improveCount", { count: g.questions.length })}
                </span>
              </h3>
              <Table>
                <thead>
                  <tr>
                    <Th>{t("diagnostics.colQuestion")}</Th>
                    <Th>{t("diagnostics.colAsked")}</Th>
                    <Th>{t("diagnostics.colMark")}</Th>
                    <Th>{t("diagnostics.colReason")}</Th>
                  </tr>
                </thead>
                <tbody>
                  {g.questions.map((q) => (
                    <tr key={q.id}>
                      <Td className="break-words">{q.text}</Td>
                      <Td className="font-mono text-xs whitespace-nowrap text-ink-muted">
                        {formatTime(q.askedAt)}
                      </Td>
                      <Td>
                        <span className="flex flex-wrap gap-1">
                          {q.rating === "down" ? (
                            <Badge tone="danger">{t("diagnostics.markDown")}</Badge>
                          ) : q.rating === "up" ? (
                            <Badge tone="supported">{t("diagnostics.markUp")}</Badge>
                          ) : (
                            <Badge>{t("diagnostics.markNone")}</Badge>
                          )}
                          {q.noEvidence && <Badge tone="stale">{t("diagnostics.noEvidence")}</Badge>}
                          {q.wrongCitation && <Badge tone="conflict">{t("diagnostics.wrongCitation")}</Badge>}
                        </span>
                      </Td>
                      <Td className="text-sm break-words text-ink-muted">{q.reason}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </section>
          ))}
        </div>
      )}
    </Sheet>
  );
}
