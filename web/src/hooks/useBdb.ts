import { useQuery } from "@tanstack/react-query";
import { getBdb } from "../api/bdbs";

/** Fetches a single bdb by ID. */
export function useBdb(id: string | undefined) {
  return useQuery({
    queryKey: ["bdbs", id],
    queryFn: () => getBdb(id!),
    enabled: !!id,
  });
}
