import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { t } from "@/copy";

export type DirectoryGroup = Schemas["DirectoryGroup"];

/** Searches the directory groups by the start of the name and returns the chosen group. */
export function DirectoryPicker({ onPick }: { onPick: (g: DirectoryGroup) => void }) {
  const [prefix, setPrefix] = useState("");
  const search = useMutation({
    mutationFn: async () =>
      unwrap(await api.GET("/api/v1/sso/directory/groups", { params: { query: { prefix } } })).groups,
  });
  return (
    <div className="flex flex-col gap-2 rounded-sm border border-rule p-3">
      <p className="label">{t("signin.pick")}</p>
      <div className="flex flex-wrap items-end gap-2">
        <Field label={t("signin.search")} className="min-w-48 flex-1">
          {(p) => (
            <Input
              {...p}
              value={prefix}
              onChange={(e) => setPrefix(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  search.mutate();
                }
              }}
            />
          )}
        </Field>
        <Button onClick={() => search.mutate()} disabled={search.isPending}>
          {t("signin.searchAction")}
        </Button>
      </div>
      {search.isError && (
        <Notice tone="danger">
          {search.error instanceof ApiError ? search.error.message : t("errors.network")}
        </Notice>
      )}
      {search.data &&
        (search.data.length === 0 ? (
          <p className="text-sm text-ink-muted">{t("signin.noGroups")}</p>
        ) : (
          <ul className="flex flex-col">
            {search.data.map((g) => (
              <li
                key={g.id}
                className="flex items-center justify-between gap-3 border-b border-rule py-1 last:border-b-0"
              >
                <span className="min-w-0">
                  <span className="block font-medium">{g.name}</span>
                  <span className="block truncate font-mono text-xs text-ink-muted">{g.id}</span>
                </span>
                <Button size="sm" variant="ghost" onClick={() => onPick(g)}>
                  {t("signin.choose")}
                </Button>
              </li>
            ))}
          </ul>
        ))}
    </div>
  );
}
