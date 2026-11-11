import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  addDataAPIKeyFormula,
  createDataAPIKey,
  listDataAPIKeys,
  removeDataAPIKeyFormula,
  revokeDataAPIKey,
} from "../api/dataAPIKeys";

/** Query key of the bdb bdbId's Data API keys. */
function dataAPIKeysKey(bdbId: string | undefined) {
  return ["bdbs", bdbId, "data-api-keys"];
}

/** Fetches the bdb's Data API keys, newest first; disabled when bdbId is undefined, as for a member not owning it. */
export function useDataAPIKeys(bdbId: string | undefined) {
  return useQuery({
    queryKey: dataAPIKeysKey(bdbId),
    queryFn: () => listDataAPIKeys(bdbId!),
    enabled: !!bdbId,
  });
}

/** Creates a Data API key and invalidates the bdb's keys cache. */
export function useCreateDataAPIKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, name, formulaIds }: { bdbId: string; name: string; formulaIds: string[] }) =>
      createDataAPIKey(bdbId, name, formulaIds),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: dataAPIKeysKey(bdbId) });
    },
  });
}

/** Revokes a Data API key and invalidates the bdb's keys cache. */
export function useRevokeDataAPIKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, keyId }: { bdbId: string; keyId: string }) => revokeDataAPIKey(bdbId, keyId),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: dataAPIKeysKey(bdbId) });
    },
  });
}

/** Lets a Data API key run a formula and invalidates the bdb's keys cache. */
export function useAddDataAPIKeyFormula() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, keyId, formulaId }: { bdbId: string; keyId: string; formulaId: string }) =>
      addDataAPIKeyFormula(bdbId, keyId, formulaId),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: dataAPIKeysKey(bdbId) });
    },
  });
}

/** Takes a formula off a Data API key and invalidates the bdb's keys cache. */
export function useRemoveDataAPIKeyFormula() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, keyId, formulaId }: { bdbId: string; keyId: string; formulaId: string }) =>
      removeDataAPIKeyFormula(bdbId, keyId, formulaId),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: dataAPIKeysKey(bdbId) });
    },
  });
}
