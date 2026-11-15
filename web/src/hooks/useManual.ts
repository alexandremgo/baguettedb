import { useQuery } from "@tanstack/react-query";
import { getManual } from "../api/docs";

/** Fetches the built-in manual of topic, kept for the session since it changes only with a release; disabled when
 *  topic is undefined. */
export function useManual(topic: string | undefined) {
  return useQuery({
    queryKey: ["docs", topic],
    queryFn: () => getManual(topic!),
    enabled: !!topic,
    staleTime: Infinity,
  });
}
