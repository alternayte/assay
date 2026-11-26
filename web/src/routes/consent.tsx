import { useQuery } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { ApiError, api, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { PublicPage } from "@/routes/public";

/** The copy of each scope that an MCP client can ask for. */
const scopeCopy: Record<string, CopyKey> = {
  read: "consent.scopeRead",
  propose: "consent.scopePropose",
  offline_access: "consent.scopeOffline",
};

/** The consent page of an MCP client: the user approves or denies the request. */
export function ConsentPage() {
  const search = useSearch({ strict: false }) as { request_id?: string };
  const requestId = search.request_id ?? "";
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const request = useQuery({
    queryKey: ["oauth-request", requestId],
    enabled: requestId !== "",
    retry: false,
    queryFn: async () =>
      unwrap(await api.GET("/api/auth/oauth2/request", { params: { query: { request_id: requestId } } })),
  });

  const decide = async (approve: boolean) => {
    setBusy(true);
    setError(undefined);
    try {
      const r = unwrap(await api.POST("/api/auth/oauth2/decide", { body: { requestId, approve } }));
      window.location.assign(r.redirectTo);
    } catch (err) {
      setError(err instanceof ApiError && err.status < 500 ? t("consent.expired") : t("errors.network"));
      setBusy(false);
    }
  };

  const data = request.data;
  // A user without a session signs in first. A user who approved this client
  // before goes back to the app with no question.
  useEffect(() => {
    if (!data) return;
    if (data.needsSignIn) {
      window.location.assign(`/sign-in?request_id=${encodeURIComponent(requestId)}`);
    } else if (data.consentGranted) {
      setBusy(true);
      api.POST("/api/auth/oauth2/decide", { body: { requestId, approve: true } }).then(
        (r) => {
          try {
            window.location.assign(unwrap(r).redirectTo);
          } catch {
            setError(t("consent.expired"));
            setBusy(false);
          }
        },
        () => {
          setError(t("errors.network"));
          setBusy(false);
        },
      );
    }
  }, [data, requestId]);

  if (requestId === "" || request.isError) {
    return (
      <PublicPage title={t("app.error")}>
        <Notice tone="danger">{t("consent.expired")}</Notice>
      </PublicPage>
    );
  }
  if (!data || data.needsSignIn || (data.consentGranted && !error)) {
    return (
      <PublicPage title={t("consent.loading")}>
        <SkeletonRows label={t("app.loading")} rows={3} />
      </PublicPage>
    );
  }
  const verified = data.clientKind === "static";
  return (
    <PublicPage
      title={t("consent.title", { name: data.clientName || data.clientHost })}
      body={t("consent.body")}
    >
      <div className="flex flex-col gap-4">
        {error && <Notice tone="danger">{error}</Notice>}
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2">
          <dt className="label pt-1">{t("consent.app")}</dt>
          <dd>{data.clientName}</dd>
          <dt className="label pt-1">{t("consent.host")}</dt>
          <dd className="font-mono text-sm leading-6 break-all" data-testid="client-host">
            {data.clientHost || t("consent.noHost")}
          </dd>
          <dt className="label pt-1">{t("consent.kind")}</dt>
          <dd>
            <Badge tone={verified ? "supported" : "stale"}>
              {verified ? t("consent.verified") : t("consent.selfRegistered")}
            </Badge>
          </dd>
        </dl>
        {!verified && <Notice tone="info">{t("consent.selfRegisteredHint")}</Notice>}
        <div className="flex flex-col gap-2">
          <p className="label">{t("consent.scopes")}</p>
          <ul className="flex flex-col gap-1 pl-5 [list-style:disc]">
            {data.scopes.map((s) => (
              <li key={s}>{scopeCopy[s] ? t(scopeCopy[s]) : s}</li>
            ))}
          </ul>
        </div>
        <p className="text-sm text-ink-muted">{t("consent.hostHint")}</p>
        <div className="flex gap-2">
          <Button variant="primary" disabled={busy} onClick={() => decide(true)}>
            {busy ? t("consent.working") : t("consent.approve")}
          </Button>
          <Button disabled={busy} onClick={() => decide(false)}>
            {t("consent.deny")}
          </Button>
        </div>
      </div>
    </PublicPage>
  );
}
