import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";

export type Me = Schemas["UserRecord"];

export const meQuery = {
  queryKey: ["me"],
  queryFn: async (): Promise<Me | null> => {
    try {
      return unwrap(await api.GET("/api/v1/me"));
    } catch (e) {
      if (e instanceof ApiError && (e.status === 401 || e.status === 403)) return null;
      throw e;
    }
  },
  staleTime: 30_000,
};

export function useMe(): Me {
  const { data } = useQuery(meQuery);
  if (!data) throw new Error("useMe needs a signed-in route");
  return data;
}

const rank = { member: 1, reviewer: 2, admin: 3 } as const;

export function atLeast(me: Me | null | undefined, role: keyof typeof rank): boolean {
  return !!me && rank[me.role] >= rank[role];
}

export function useSignOut() {
  const qc = useQueryClient();
  return async () => {
    await api.POST("/api/auth/sign-out");
    qc.clear();
    window.location.assign("/sign-in");
  };
}
