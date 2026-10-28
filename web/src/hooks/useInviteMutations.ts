import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createInvite, acceptInvite, revokeInvite } from "../api/bdbs";
import type { CreateInviteRequest } from "../api/bdbs";

/** Creates an invite for a bdb. */
export function useCreateInvite() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, data }: { bdbId: string; data: CreateInviteRequest }) =>
      createInvite(bdbId, data),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs", bdbId, "invites"] });
    },
  });
}

/** Accepts an invite by token. */
export function useAcceptInvite() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => acceptInvite(token),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
    },
  });
}

/** Revokes a pending invite. */
export function useRevokeInvite() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, inviteId }: { bdbId: string; inviteId: string }) =>
      revokeInvite(bdbId, inviteId),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs", bdbId, "invites"] });
    },
  });
}
