/**
 * Renders markdown (GitHub-flavored: lists, tables, task lists, strikethrough) via react-markdown,
 * which emits React elements — raw HTML in the source is skipped, never injected, so there is no
 * sanitization surface. Images are dropped so a body can't make the reader fetch a third-party URL.
 * Links open in a new tab.
 */

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import styles from "./Markdown.module.css";

interface MarkdownProps {
  text: string;
}

export function Markdown({ text }: MarkdownProps) {
  return (
    <div className={styles.markdown}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        disallowedElements={["img"]}
        components={{
          a: ({ href, children }) => (
            <a href={href} target="_blank" rel="noreferrer noopener">
              {children}
            </a>
          ),
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
