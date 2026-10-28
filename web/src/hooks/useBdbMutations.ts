import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  createBdb,
  updateBdb,
  deleteBdb,
  restoreBdb,
  reorderBdbs,
  updateMemberRole,
  removeMember,
} from "../api/bdbs";
import type { Bdb, CreateBdbRequest, UpdateBdbRequest } from "../api/bdbs";
import { createBdbFromStarter } from "../api/starters";
import type { CreateFromStarterRequest } from "../api/starters";

/** Creates a new bdb and invalidates the bdbs list cache. */
export function useCreateBdb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateBdbRequest) => createBdb(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
    },
  });
}

/** Creates a bdb seeded from a starter and invalidates the bdbs list cache. */
export function useCreateBdbFromStarter() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateFromStarterRequest) => createBdbFromStarter(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
    },
  });
}

/** Updates a bdb and invalidates both the list and detail caches. */
export function useUpdateBdb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdateBdbRequest }) => updateBdb(id, data),
    onSuccess: (_, { id }) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
      queryClient.invalidateQueries({ queryKey: ["bdbs", id] });
    },
  });
}

/** Deletes a bdb and invalidates the bdbs list cache. */
export function useDeleteBdb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteBdb(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
    },
  });
}

/** Restores a soft-deleted bdb within its grace period and refreshes the list + detail caches. */
export function useRestoreBdb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => restoreBdb(id),
    onSuccess: (_, id) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
      queryClient.invalidateQueries({ queryKey: ["bdbs", id] });
    },
  });
}

/**
 * Rearranges the caller's bdb list. The cache is rewritten optimistically so the list settles under
 * the pointer instead of snapping back for a round-trip, and is restored if the request fails.
 */
export function useReorderBdbs() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (bdbIds: string[]) => reorderBdbs(bdbIds),
    onMutate: async (bdbIds: string[]) => {
      // Cancels in-flight list fetches so a response from before the drag can't overwrite it.
      await queryClient.cancelQueries({ queryKey: ["bdbs"] });
      const previous = queryClient.getQueryData<Bdb[]>(["bdbs"]);

      if (previous) {
        const byId = new Map(previous.map((bdb) => [bdb.id, bdb]));
        const reordered = bdbIds
          .map((id) => byId.get(id))
          .filter((bdb): bdb is Bdb => bdb !== undefined);
        queryClient.setQueryData<Bdb[]>(["bdbs"], reordered);
      }

      return { previous };
    },
    onError: (_err, _bdbIds, context) => {
      if (context?.previous) {
        queryClient.setQueryData<Bdb[]>(["bdbs"], context.previous);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ["bdbs"] });
    },
  });
}

/** Updates a member's role and invalidates the bdb detail cache. */
export function useUpdateMemberRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, userId, role }: { bdbId: string; userId: string; role: string }) =>
      updateMemberRole(bdbId, userId, { role }),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs", bdbId] });
    },
  });
}

/** Removes a member from a bdb and invalidates the bdb detail cache. */
export function useRemoveMember() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ bdbId, userId }: { bdbId: string; userId: string }) =>
      removeMember(bdbId, userId),
    onSuccess: (_, { bdbId }) => {
      queryClient.invalidateQueries({ queryKey: ["bdbs", bdbId] });
    },
  });
}
