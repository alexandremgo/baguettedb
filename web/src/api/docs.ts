/** Built-in manuals API: the reference documents agents read, such as the Data API's, in Markdown. */

import { get } from "./client";

/** Returns the manual of topic, in Markdown, rendered with the caller's limits. */
export async function getManual(topic: string): Promise<string> {
  const res = await get<{ topic: string; content: string }>(`/api/databases/v1/docs/${encodeURIComponent(topic)}`);
  return res.content;
}
