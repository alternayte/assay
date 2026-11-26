import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { api, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { t } from "@/copy";
import { History, useSaveSetting, useSetting } from "@/routes/settings/index";

type Config = { mcpOAuth: boolean };

const statusQuery = {
  queryKey: ["mcp-oauth"],
  queryFn: async () => unwrap(await api.GET("/api/v1/mcp/oauth")),
};

/** Settings, Agents: OAuth sign-in of MCP clients. */
export function AgentsSettingsPage() {
  const qc = useQueryClient();
  const setting = useSetting("agents");
  const status = useQuery(statusQuery);
  const [value, setValue] = useState<Config>();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (setting.data) setValue(setting.data.value as Config);
  }, [setting.data]);
  const save = useSaveSetting(
    "agents",
    (s) => {
      qc.invalidateQueries({ queryKey: ["mcp-oauth"] });
      qc.invalidateQueries({ queryKey: ["connect"] });
      setMsg({ tone: "success", text: t("agentsSettings.saved", { revision: s.revision }) });
    },
    (text) => setMsg({ tone: "danger", text }),
  );

  if (setting.isPending || status.isPending || !value) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || status.isError || !setting.data)
    return <Notice tone="danger">{t("app.error")}</Notice>;
  const locked = status.data.lockedBy !== "";
  const revision = setting.data.revision;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    save.mutate({ value, revision });
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-6">
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      <Sheet title={t("agentsSettings.title")} description={t("agentsSettings.body")}>
        <div className="flex flex-col gap-2">
          <label className="flex items-center gap-2 text-base">
            <input
              type="checkbox"
              disabled={locked}
              checked={locked ? status.data.on : value.mcpOAuth}
              onChange={(e) => setValue({ ...value, mcpOAuth: e.target.checked })}
            />
            {t("agentsSettings.oauthOn")}
          </label>
          <p className="text-sm text-ink-muted">
            {locked
              ? t("agentsSettings.locked", { name: status.data.lockedBy })
              : t("agentsSettings.oauthHint")}
          </p>
        </div>
      </Sheet>
      <div>
        <Button type="submit" variant="primary" disabled={locked || save.isPending}>
          {t("agentsSettings.save")}
        </Button>
      </div>
      <History settingKey="agents" />
    </form>
  );
}
