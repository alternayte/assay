import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";

export function DiagnosticsPage() {
  const run = useMutation({ mutationFn: async () => unwrap(await api.POST("/api/v1/diagnostics")) });
  return (
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
  );
}
