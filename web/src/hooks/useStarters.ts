import { useQuery } from "@tanstack/react-query";
import { listStarters } from "../api/starters";

/** Fetches the starter catalog. The catalog only changes with a deploy, so it stays fresh for the session. */
export function useStarters() {
  return useQuery({
    queryKey: ["starters"],
    queryFn: listStarters,
    staleTime: Infinity,
  });
}
