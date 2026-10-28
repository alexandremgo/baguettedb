import { useQuery } from "@tanstack/react-query";
import { getInviteByToken, listInvites } from "../api/bdbs";

/** Fetches invite details by token. */
export function useInviteDetails(token: string | undefined) {
  return useQuery({
    queryKey: ["invite", token],
    queryFn: () => getInviteByToken(token!),
    enabled: !!token,
    retry: false,
  });
}

/** Fetches all invites for a bdb. */
export function useBdbInvites(bdbId: string | undefined) {
  return useQuery({
    queryKey: ["bdbs", bdbId, "invites"],
    queryFn: () => listInvites(bdbId!),
    enabled: !!bdbId,
  });
}
