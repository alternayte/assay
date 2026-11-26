import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { DirectoryPicker } from "@/components/directory-picker";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { t } from "@/copy";
import { formatTime } from "@/lib/format";
import { History, useSaveSetting, useSetting } from "@/routes/settings/index";

type Config = Schemas["SignInConfig"];
type Status = Schemas["SSOStatus"];

const statusQuery = {
  queryKey: ["sso"],
  queryFn: async () => unwrap(await api.GET("/api/v1/sso")),
};

/** The hint of a field: the environment variable that locks it, or the normal hint. */
function hintOf(status: Status, field: string, hint?: string): string | undefined {
  const name = status.locks[field];
  return name ? t("signin.locked", { name }) : hint;
}

export function SignInSettingsPage() {
  const qc = useQueryClient();
  const setting = useSetting("signin");
  const status = useQuery(statusQuery);
  const [value, setValue] = useState<Config>();
  const [secret, setSecret] = useState("");
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (setting.data) setValue(setting.data.value as Config);
  }, [setting.data]);
  const fail = (text: string) => setMsg({ tone: "danger", text });
  const save = useSaveSetting(
    "signin",
    (s) => {
      qc.invalidateQueries({ queryKey: ["sso"] });
      setMsg({ tone: "success", text: t("signin.saved", { revision: s.revision }) });
    },
    fail,
  );
  const saveSecret = useMutation({
    mutationFn: async (v: string) => unwrap(await api.PUT("/api/v1/sso/secret", { body: { secret: v } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sso"] }),
  });

  if (setting.isPending || status.isPending || !value) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || status.isError || !setting.data)
    return <Notice tone="danger">{t("app.error")}</Notice>;
  const st = status.data;
  const revision = setting.data.revision;
  const locked = (field: string) => field in st.locks;
  // A locked field shows the value of its environment variable.
  const shown = <K extends keyof Config>(k: K): Config[K] => (locked(k) ? st.effective[k] : value[k]);
  const set =
    <K extends keyof Config>(k: K) =>
    (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
      setValue({ ...value, [k]: k === "intervalMinutes" ? Number(e.target.value) : e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    try {
      if (secret && !locked("clientSecret")) {
        await saveSecret.mutateAsync(secret);
        setSecret("");
      }
      save.mutate({ value, revision });
    } catch (err) {
      fail(err instanceof ApiError ? err.message : t("errors.network"));
    }
  };

  const text = (k: "issuer" | "clientId" | "buttonLabel", label: string, hint?: string, mono = false) => (
    <Field label={label} hint={hintOf(st, k, hint)}>
      {(p) => (
        <Input
          {...p}
          className={mono ? "font-mono" : undefined}
          disabled={locked(k)}
          value={shown(k)}
          onChange={set(k)}
        />
      )}
    </Field>
  );

  return (
    <form onSubmit={submit} className="flex flex-col gap-6">
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      <Sheet title={t("signin.title")} description={t("signin.body")}>
        <div className="flex flex-col gap-4">
          {text("issuer", t("signin.issuer"), t("signin.issuerHint"), true)}
          <div className="grid gap-4 sm:grid-cols-2">
            {text("clientId", t("signin.clientId"), undefined, true)}
            <Field
              label={t("signin.clientSecret")}
              hint={hintOf(
                st,
                "clientSecret",
                st.secretSet ? t("signin.clientSecretSet") : t("signin.clientSecretHint"),
              )}
            >
              {(p) => (
                <Input
                  {...p}
                  type="password"
                  autoComplete="new-password"
                  disabled={locked("clientSecret")}
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                />
              )}
            </Field>
          </div>
          {text("buttonLabel", t("signin.buttonLabel"), t("signin.buttonLabelHint"))}
          <div className="flex flex-col gap-1">
            <p className="label">{t("signin.callback")}</p>
            <p className="rounded-sm bg-paper-sunk px-2 py-1 font-mono text-sm break-all">{st.callbackUrl}</p>
            <p className="text-sm text-ink-muted">{t("signin.callbackHint")}</p>
          </div>
          {st.secretSet && !locked("clientSecret") && (
            <div>
              <Button
                size="sm"
                onClick={() =>
                  saveSecret.mutate("", {
                    onSuccess: () => setMsg({ tone: "success", text: t("signin.secretRemoved") }),
                  })
                }
              >
                {t("signin.removeSecret")}
              </Button>
            </div>
          )}
        </div>
      </Sheet>

      <Sheet title={t("signin.passwordTitle")} description={t("signin.passwordBody")}>
        <div className="flex flex-col gap-2">
          <label className="flex items-center gap-2 text-base">
            <input
              type="checkbox"
              disabled={locked("passwordSignIn")}
              checked={shown("passwordSignIn")}
              onChange={(e) => setValue({ ...value, passwordSignIn: e.target.checked })}
            />
            {t("signin.passwordOn")}
          </label>
          {locked("passwordSignIn") && (
            <p className="text-sm text-ink-muted">{hintOf(st, "passwordSignIn")}</p>
          )}
          {!st.adminLinked && <p className="text-sm text-ink-muted">{t("signin.passwordNeedsAdmin")}</p>}
        </div>
      </Sheet>

      <Sheet title={t("signin.accessTitle")} description={t("signin.accessBody")}>
        <div className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("signin.accessGroup")} hint={hintOf(st, "accessGroup", t("signin.accessEmpty"))}>
              {(p) => (
                <Input
                  {...p}
                  className="font-mono"
                  disabled={locked("accessGroup")}
                  value={shown("accessGroup")}
                  onChange={set("accessGroup")}
                />
              )}
            </Field>
            <Field label={t("signin.accessGroupName")}>
              {(p) => (
                <Input
                  {...p}
                  disabled={locked("accessGroup")}
                  value={shown("accessGroupName")}
                  onChange={set("accessGroupName")}
                />
              )}
            </Field>
          </div>
          {st.directoryOn && !locked("accessGroup") && (
            <DirectoryPicker
              onPick={(g) => setValue({ ...value, accessGroup: g.id, accessGroupName: g.name })}
            />
          )}
        </div>
      </Sheet>

      <Sheet title={t("signin.directoryTitle")} description={t("signin.directoryBody")}>
        <div className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-[1fr_12rem]">
            <Field label={t("signin.directory")} hint={hintOf(st, "directory", t("signin.directoryHint"))}>
              {(p) => (
                <Select
                  {...p}
                  disabled={locked("directory")}
                  value={shown("directory")}
                  onChange={set("directory")}
                >
                  <option value="none">{t("signin.directoryNone")}</option>
                  <option value="entra">{t("signin.directoryEntra")}</option>
                </Select>
              )}
            </Field>
            <Field
              label={t("signin.interval")}
              hint={hintOf(st, "intervalMinutes", t("signin.intervalHint"))}
            >
              {(p) => (
                <Input
                  {...p}
                  type="number"
                  min={5}
                  max={1440}
                  required
                  className="font-mono"
                  disabled={locked("intervalMinutes")}
                  value={shown("intervalMinutes")}
                  onChange={set("intervalMinutes")}
                />
              )}
            </Field>
          </div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
            <dt className="text-ink-muted">{t("signin.lastSuccess")}</dt>
            <dd>
              {st.directory.lastSuccessAt ? (
                <span className="font-mono">{formatTime(st.directory.lastSuccessAt)}</span>
              ) : (
                t("signin.never")
              )}
            </dd>
            {st.directory.lastError && (
              <>
                <dt className="text-ink-muted">{t("signin.lastError")}</dt>
                <dd className="font-mono break-all">{st.directory.lastError}</dd>
              </>
            )}
          </dl>
          {st.directory.lastError && st.directory.lastErrorAt && (
            <Notice tone="danger">
              {t("signin.pollFailed", { time: formatTime(st.directory.lastErrorAt) })}
            </Notice>
          )}
        </div>
      </Sheet>

      <div>
        <Button type="submit" variant="primary" disabled={save.isPending || saveSecret.isPending}>
          {t("signin.save")}
        </Button>
      </div>
      <History settingKey="signin" />
    </form>
  );
}
