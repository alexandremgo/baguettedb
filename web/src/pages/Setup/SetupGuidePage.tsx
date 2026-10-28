import { Link } from "react-router-dom";
import { useAuth } from "../../auth";
import { CopyableCode } from "../../components/CopyableCode";
import { getConfig, isTokenMode } from "../../config";
import { claudeCodeCommand, mcpConfigEntry } from "../../utils/mcpSnippets";
import styles from "./SetupGuidePage.module.css";

/**
 * MCP setup guide: OAuth clients authenticate when prompted, while token mode sends a personal access token, created
 * in Settings, as a command or as the entry of a client's configuration file. In token mode, an account other than the
 * owner has no local registration: an assistant on the serving machine acts as the owner.
 */
export function SetupGuidePage() {
  const { mcpUrl, ownerEmail } = getConfig();
  const { user } = useAuth();
  const notOwner = isTokenMode() && ownerEmail !== undefined && user !== null && user.email !== ownerEmail;

  return (
    <div className={styles.container}>
      <h1 className={styles.title}>Setup Guide</h1>
      <p className={styles.subtitle}>
        Connect your AI assistant to BaguetteDB using the Model Context
        Protocol (MCP).
      </p>

      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>Quick Start</h2>
        {isTokenMode() ? (
          <ol className={styles.steps}>
            <li>
              Create a personal access token for your assistant in <Link to="/settings">Settings</Link>
              {notOwner ? (
                "."
              ) : (
                <>
                  , or on the machine serving BaguetteDB: <CopyableCode text="bdb tokens create <token-name>" />
                </>
              )}
            </li>
            <li>
              Add a new MCP server with URL <CopyableCode text={mcpUrl} /> and the header{" "}
              <code>Authorization: Bearer &lt;token&gt;</code>. For example, with Claude Code:{" "}
              <CopyableCode text={claudeCodeCommand(mcpUrl, "<token>")} />{" "}
              or, in a client's configuration file such as <code>~/.claude.json</code> or a project&apos;s{" "}
              <code>.mcp.json</code>:
              <CopyableCode text={mcpConfigEntry(mcpUrl, "<token>")} block />
              {!notOwner && (
                <>
                  On the serving machine, <code>bdb mcp config --client &lt;client&gt; --http</code> prints this for
                  each client.
                </>
              )}
            </li>
            <li>Start chatting — your assistant can now read and write your data.</li>
          </ol>
        ) : (
          <ol className={styles.steps}>
            <li>
              Open your MCP client's server configuration (e.g. Claude Desktop,
              Cursor, or any MCP-compatible client).
            </li>
            <li>
              Add a new MCP server with URL:{" "}
              <CopyableCode text={mcpUrl} />
            </li>
            <li>
              Authenticate with your BaguetteDB credentials when prompted.
            </li>
            <li>Start chatting — your assistant can now read and write your data.</li>
          </ol>
        )}
      </section>
      {isTokenMode() && !notOwner && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>On the same machine</h2>
          <p className={styles.subtitle}>
            An assistant running on the machine that serves BaguetteDB needs no token: register the local instance
            with <CopyableCode text="bdb mcp config --client <client>" />
          </p>
        </section>
      )}
    </div>
  );
}
