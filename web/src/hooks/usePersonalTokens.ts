import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createPersonalToken, listPersonalTokens, revokePersonalToken } from "../api/personalTokens";

const personalTokensKey = ["personal-tokens"];

/** Fetches the signed-in user's personal access tokens that are not revoked, newest first. */
export function usePersonalTokens() {
  return useQuery({ queryKey: personalTokensKey, queryFn: listPersonalTokens });
}

/** Creates a personal access token and invalidates the tokens list cache. */
export function useCreatePersonalToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ name, expiresAt }: { name: string; expiresAt?: string }) => createPersonalToken(name, expiresAt),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: personalTokensKey });
    },
  });
}

/** Revokes a personal access token and invalidates the tokens list cache. */
export function useRevokePersonalToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => revokePersonalToken(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: personalTokensKey });
    },
  });
}
