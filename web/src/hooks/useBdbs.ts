import { useQuery } from "@tanstack/react-query";
import { listBdbs } from "../api/bdbs";

/** Fetches all bdbs the user belongs to. */
export function useBdbs() {
  return useQuery({
    queryKey: ["bdbs"],
    queryFn: listBdbs,
  });
}
