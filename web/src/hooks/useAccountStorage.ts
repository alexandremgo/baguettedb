import { useQuery } from "@tanstack/react-query";
import { getAccountStorage } from "../api/databases";

/** Fetches the calling user's account-wide storage usage across every bdb they own. */
export function useAccountStorage(enabled = true) {
  return useQuery({
    queryKey: ["account-storage"],
    queryFn: getAccountStorage,
    enabled,
  });
}
