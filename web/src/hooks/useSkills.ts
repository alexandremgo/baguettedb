import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteSkill, listSkills } from "../api/skills";

/** Fetches all skills (with body) for a bdb. */
export function useSkills(bdbId: string | undefined) {
  return useQuery({
    queryKey: ["skills", bdbId],
    queryFn: () => listSkills(bdbId!),
    enabled: !!bdbId,
  });
}

/** Deletes a skill by name or UUID and invalidates the list cache on success. */
export function useDeleteSkill(bdbId: string | undefined) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (ref: string) => deleteSkill(bdbId!, ref),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["skills", bdbId] });
    },
  });
}
