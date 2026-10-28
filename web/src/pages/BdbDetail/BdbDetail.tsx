/** Bdb detail page with members list, invite management, and database tables viewer. */

import { useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { Card } from "../../components/Card";
import { Dialog } from "../../components/Dialog";
import { ExportBdbDialog } from "../../components/Export";
import { Menu } from "../../components/Menu";
import { Select } from "../../components/Select";
import { Table } from "../../components/Table";
import { useToast } from "../../components/Toast";
import { EmptyState } from "../../components/EmptyState";
import { SchemaList } from "../../components/SchemaList";
import { Skeleton, SkeletonRows } from "../../components/Skeleton";
import { BookOpen, SquareKanban, Table2, Users, X } from "../../components/icons";
import { TruncatedCell } from "../../components/TruncatedCell/TruncatedCell";
import { bdbCanWrite } from "../../api/bdbs";
import { useBdb } from "../../hooks/useBdb";
import { useBdbInvites } from "../../hooks/useInvites";
import { useCreateInvite, useRevokeInvite } from "../../hooks/useInviteMutations";
import { useRestoreBdb, useUpdateMemberRole, useRemoveMember } from "../../hooks/useBdbMutations";
import { useTables, useTableSchema, useTableData } from "../../hooks/useTables";
import { formatCellValue, isNullValue } from "../../utils/cellValue";
import { formatPurgeDate } from "../../utils/date";
import { DeleteBdbModal } from "./DeleteBdbModal";
import { EditBdbModal } from "./EditBdbModal";
import { FormulasTab } from "./FormulasTab";
import { SkillsTab } from "./SkillsTab";
import { bdbOwnerAttribution, roleBadgeVariant } from "../../utils/bdbDisplay";
import styles from "./BdbDetail.module.css";

function formatExpiration(expiresAt: string): string {
  const now = new Date();
  const expires = new Date(expiresAt);
  const diffMs = expires.getTime() - now.getTime();

  if (diffMs <= 0) return "expired";

  const hours = Math.floor(diffMs / (1000 * 60 * 60));
  if (hours < 1) return "expires in <1h";
  if (hours < 24) return `expires in ${hours}h`;
  const days = Math.floor(hours / 24);
  return `expires in ${days}d`;
}

type TabId = "tables" | "formulas" | "skills" | "members";

export function BdbDetail() {
  const { id } = useParams<{ id: string }>();
  const [activeTab, setActiveTab] = useState<TabId>("formulas");
  const [selectedTable, setSelectedTable] = useState<string | null>(null);
  const [rowQuery, setRowQuery] = useState("");
  const [rowLimit, setRowLimit] = useState(50);

  // Picks a table and resets the per-table view state alongside it.
  function selectTable(name: string | null) {
    setSelectedTable(name);
    setRowQuery("");
    setRowLimit(50);
  }

  // A bdb switch invalidates the table selection — without this, the previous bdb's table panel
  // lingers next to the new bdb's table list. Adjusted during render so the stale panel never paints.
  const [lastBdbId, setLastBdbId] = useState(id);
  if (id !== lastBdbId) {
    setLastBdbId(id);
    selectTable(null);
  }
  const [inviteDialogOpen, setInviteDialogOpen] = useState(false);
  const [inviteRole, setInviteRole] = useState("editor");
  const [generatedInviteUrl, setGeneratedInviteUrl] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [exportDialogOpen, setExportDialogOpen] = useState(false);

  const { toast } = useToast();
  const { data: bdb, isLoading, error } = useBdb(id);
  const { data: tables = [], isLoading: tablesLoading } = useTables(id);
  const { data: tableSchema } = useTableSchema(id, selectedTable);
  const { data: tableData } = useTableData(id, selectedTable, rowLimit);

  const isOwner = bdb?.my_role === "owner";
  const isSoftDeleted = Boolean(bdb?.deleted_at);
  const canManage = isOwner && !isSoftDeleted;
  const { data: invites = [] } = useBdbInvites(isOwner ? id : undefined);

  const createInviteMutation = useCreateInvite();
  const revokeInviteMutation = useRevokeInvite();
  const updateRoleMutation = useUpdateMemberRole();
  const removeMemberMutation = useRemoveMember();
  const restoreMutation = useRestoreBdb();

  function handleRestore() {
    if (!id) return;
    restoreMutation.mutate(id, {
      onSuccess: () => toast("bdb restored.", "success"),
      onError: (err) => toast(err instanceof Error ? err.message : "Failed to restore bdb", "error"),
    });
  }

  if (isLoading) {
    return (
      <div>
        <Skeleton className={styles.skeletonTitle} />
        <SkeletonRows />
      </div>
    );
  }

  if (error || !bdb) {
    return <p className={styles.error}>{error instanceof Error ? error.message : "bdb not found"}</p>;
  }

  function handleGenerateInvite() {
    if (!id) return;
    createInviteMutation.mutate(
      { bdbId: id, data: { role: inviteRole } },
      {
        onSuccess: (invite) => {
          setGeneratedInviteUrl(invite.invite_url);
        },
        onError: (err) => {
          toast(err instanceof Error ? err.message : "Failed to create invite", "error");
        },
      },
    );
  }

  function handleCopyLink() {
    if (!generatedInviteUrl) return;
    navigator.clipboard.writeText(generatedInviteUrl).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  function handleCloseInviteDialog() {
    setInviteDialogOpen(false);
    setGeneratedInviteUrl(null);
    setCopied(false);
    setInviteRole("editor");
  }

  function handleRevokeInvite(inviteId: string) {
    if (!id || !confirm("Revoke this invite? The link will no longer work.")) return;
    revokeInviteMutation.mutate(
      { bdbId: id, inviteId },
      {
        onSuccess: () => toast("Invite revoked", "success"),
        onError: (err) => toast(err instanceof Error ? err.message : "Failed to revoke invite", "error"),
      },
    );
  }

  function handleRoleChange(userId: string, newRole: string) {
    if (!id) return;
    updateRoleMutation.mutate(
      { bdbId: id, userId, role: newRole },
      {
        onSuccess: () => toast("Role updated", "success"),
        onError: (err) => toast(err instanceof Error ? err.message : "Failed to update role", "error"),
      },
    );
  }

  function handleRemoveMember(userId: string) {
    if (!id || !confirm("Remove this member from the bdb?")) return;
    removeMemberMutation.mutate(
      { bdbId: id, userId },
      {
        onSuccess: () => toast("Member removed", "success"),
        onError: (err) => toast(err instanceof Error ? err.message : "Failed to remove member", "error"),
      },
    );
  }

  const pendingInvites = invites.filter((inv) => inv.status === "pending");

  return (
    <div>
      {isSoftDeleted && (
        <div className={styles.softDeleteBanner} role="status">
          <span className={styles.softDeleteBannerText}>
            This bdb is scheduled for deletion on {formatPurgeDate(bdb.purge_at)}. Restore to keep it.
          </span>
          {isOwner && (
            <Button
              size="sm"
              variant="secondary"
              onClick={handleRestore}
              loading={restoreMutation.isPending}
            >
              Restore
            </Button>
          )}
        </div>
      )}
      <div className={styles.header}>
        <h1 className={styles.title}>{bdb.name}</h1>
        <Badge variant={roleBadgeVariant(bdb.my_role ?? "")}>
          {bdb.my_role ?? "member"}
        </Badge>
        {bdbOwnerAttribution(bdb) && (
          <span className={styles.ownerAttribution}>by {bdbOwnerAttribution(bdb)}</span>
        )}
        {canManage && (
          <div className={styles.headerActions}>
            <Menu
              aria-label="bdb actions"
              items={[
                { label: "Export…", onSelect: () => setExportDialogOpen(true) },
                { label: "Edit…", onSelect: () => setEditDialogOpen(true) },
                { label: "Delete…", onSelect: () => setDeleteDialogOpen(true), destructive: true },
              ]}
            />
          </div>
        )}
      </div>
      {bdb.description ? (
        <p className={styles.description}>{bdb.description}</p>
      ) : (
        canManage && (
          <button
            type="button"
            className={styles.addDescription}
            onClick={() => setEditDialogOpen(true)}
          >
            Add a description…
          </button>
        )
      )}

      <div className={styles.tabBar}>
        {(
          [
            { id: "formulas", label: "Formulas", Icon: SquareKanban },
            { id: "tables", label: "Tables", Icon: Table2 },
            { id: "skills", label: "Skills", Icon: BookOpen },
            { id: "members", label: "Members", Icon: Users },
          ] as { id: TabId; label: string; Icon: typeof SquareKanban }[]
        ).map((tab) => (
          <button
            key={tab.id}
            className={[styles.tab, activeTab === tab.id ? styles.tabActive : ""].join(" ")}
            onClick={() => setActiveTab(tab.id)}
          >
            <tab.Icon size={16} aria-hidden="true" />
            {tab.label}
          </button>
        ))}
      </div>

      <div className={styles.sections}>
        {activeTab === "members" && (
          <>
        {/* Members */}
        <section>
          <div className={styles.sectionHeader}>
            <h2 className={styles.sectionTitle}>
              Members ({bdb.members.length})
            </h2>
            {canManage && (
              <Button size="sm" onClick={() => setInviteDialogOpen(true)}>
                Invite
              </Button>
            )}
          </div>
          <Card>
            <Table
              columns={[
                { key: "user_id", header: "Member", render: (m) => {
                  const name = [m.first_name, m.last_name].filter(Boolean).join(" ");
                  return (
                    <div>
                      {name && <span className={styles.memberName}>{name}</span>}
                      {m.email && <span className={styles.memberEmail}>{m.email}</span>}
                      {!name && !m.email && <span className={styles.userId}>{m.user_id}</span>}
                    </div>
                  );
                }},
                { key: "role", header: "Role", render: (m) => {
                  if (!canManage || m.role === "owner") {
                    return <Badge variant={roleBadgeVariant(m.role)}>{m.role}</Badge>;
                  }
                  return (
                    <Select
                      options={[
                        { value: "editor", label: "editor" },
                        { value: "viewer", label: "viewer" },
                      ]}
                      value={m.role}
                      onChange={(e) => handleRoleChange(m.user_id, e.target.value)}
                      className={styles.roleSelect}
                    />
                  );
                }},
                ...(canManage ? [{
                  key: "actions" as const,
                  header: "",
                  render: (m: { user_id: string; role: string }) => {
                    if (m.role === "owner") return null;
                    return (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleRemoveMember(m.user_id)}
                      >
                        Remove
                      </Button>
                    );
                  },
                }] : []),
              ]}
              data={bdb.members}
              rowKey={(m) => m.user_id}
            />
          </Card>
        </section>

            {/* Invites (owner only, hidden while in grace period — restore first) */}
            {canManage && pendingInvites.length > 0 && (
          <section>
            <h2 className={styles.sectionTitle}>
              Pending Invites ({pendingInvites.length})
            </h2>
            <Table
              className={styles.invitesTable}
              columns={[
                { key: "role", header: "Role", render: (inv) => (
                  <Badge variant={roleBadgeVariant(inv.role)}>{inv.role}</Badge>
                )},
                { key: "expires_at", header: "Expiration", render: (inv) => (
                  <span className={styles.expiration}>{formatExpiration(inv.expires_at)}</span>
                )},
                { key: "invite_url", header: "Link", render: (inv) => (
                  <TruncatedCell maxLength={20} copyable className={styles.inviteLink}>
                    {inv.invite_url}
                  </TruncatedCell>
                )},
                { key: "actions", header: "", render: (inv) => (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleRevokeInvite(inv.id)}
                  >
                    Revoke
                  </Button>
                )},
              ]}
              data={pendingInvites}
              rowKey={(inv) => inv.id}
            />
              </section>
            )}
          </>
        )}

        {activeTab === "tables" && (
        /* Tables */
        <section>
          <h2 className={styles.sectionTitle}>Tables</h2>
          {tablesLoading ? (
            <SkeletonRows />
          ) : tables.length === 0 ? (
            <EmptyState
              icon={Table2}
              title="No tables yet."
              hint="Ask your assistant to set this bdb up."
            />
          ) : (
            <div className={styles.dbLayout}>
              <div className={styles.tableList}>
                {tables.map((t) => (
                  <button
                    key={t}
                    className={[
                      styles.tableItem,
                      t === selectedTable ? styles.tableItemActive : "",
                    ].join(" ")}
                    onClick={() => selectTable(t)}
                  >
                    {t}
                  </button>
                ))}
              </div>

              <div className={styles.tableContent}>
                {!selectedTable && (
                  <EmptyState icon={Table2} title="Select a table to view its data." />
                )}

                {selectedTable && tableSchema && (
                  <div className={styles.schemaSection}>
                    <h3 className={styles.tableTitle}>{selectedTable}</h3>
                    <SchemaList key={selectedTable} columns={tableSchema.columns} />
                  </div>
                )}

                {selectedTable && !tableData && <SkeletonRows />}

                {selectedTable && tableData && (() => {
                  const hasMore = tableData.rows.length > rowLimit;
                  const allRows = tableData.rows.slice(0, rowLimit).map((row) => {
                    const obj: Record<string, unknown> = {};
                    tableData.columns.forEach((col, i) => {
                      obj[col.name] = row[i]?.value ?? null;
                    });
                    return obj;
                  });
                  const term = rowQuery.trim().toLowerCase();
                  const shownRows =
                    term === ""
                      ? allRows
                      : allRows.filter((obj) =>
                          tableData.columns.some((col) =>
                            formatCellValue(obj[col.name]).toLowerCase().includes(term),
                          ),
                        );
                  const countLabel =
                    term !== ""
                      ? `${shownRows.length} of ${allRows.length} row${allRows.length !== 1 ? "s" : ""}`
                      : `${allRows.length}${hasMore ? "+" : ""} row${allRows.length !== 1 ? "s" : ""}`;
                  return (
                    <div className={styles.dataSection}>
                      <div className={styles.dataHeader}>
                        <div className={styles.rowFilterWrap}>
                          <input
                            type="search"
                            className={styles.rowFilter}
                            placeholder="Filter rows…"
                            aria-label="Filter the loaded rows"
                            value={rowQuery}
                            onChange={(e) => setRowQuery(e.target.value)}
                            onKeyDown={(e) => {
                              if (e.key === "Escape") setRowQuery("");
                            }}
                          />
                          {rowQuery !== "" && (
                            <button
                              type="button"
                              className={styles.rowFilterClear}
                              aria-label="Clear filter"
                              onClick={() => setRowQuery("")}
                            >
                              <X size={14} aria-hidden="true" />
                            </button>
                          )}
                        </div>
                        <span className={styles.dataLabel}>{countLabel}</span>
                      </div>
                      <Card>
                        <Table
                          className={styles.dataTable}
                          columns={tableData.columns.map((col) => ({
                            key: col.name,
                            header: col.name,
                            render: (row: Record<string, unknown>) => {
                              const val = row[col.name];
                              if (isNullValue(val)) {
                                return <span className={styles.nullValue}>NULL</span>;
                              }
                              return formatCellValue(val);
                            },
                          }))}
                          data={shownRows}
                          rowKey={(_, i) => String(i)}
                          emptyMessage={term !== "" ? "No rows match the filter" : "Table is empty"}
                        />
                      </Card>
                      {hasMore && (
                        <div className={styles.loadMoreBar}>
                          <Button size="sm" variant="secondary" onClick={() => setRowLimit((l) => l + 50)}>
                            Load more
                          </Button>
                          {term !== "" && (
                            <span className={styles.loadMoreHint}>
                              The filter only covers the loaded rows.
                            </span>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })()}
              </div>
            </div>
          )}
        </section>
        )}

        {activeTab === "formulas" && id && <FormulasTab bdbId={id} />}

        {activeTab === "skills" && id && (
          <SkillsTab
            bdbId={id}
            canEdit={bdbCanWrite(bdb)}
          />
        )}
      </div>

      {canManage && (
        <DeleteBdbModal
          bdb={bdb}
          open={deleteDialogOpen}
          onClose={() => setDeleteDialogOpen(false)}
        />
      )}

      {canManage && (
        <EditBdbModal
          bdb={bdb}
          open={editDialogOpen}
          onClose={() => setEditDialogOpen(false)}
        />
      )}

      {canManage && id && (
        <ExportBdbDialog
          bdbId={id}
          bdbName={bdb.name}
          open={exportDialogOpen}
          onClose={() => setExportDialogOpen(false)}
        />
      )}

      {/* Invite creation dialog */}
      <Dialog open={inviteDialogOpen} onClose={handleCloseInviteDialog} title="Invite to bdb">
        {!generatedInviteUrl ? (
          <div className={styles.inviteForm}>
            <Select
              label="Role"
              options={[
                { value: "editor", label: "Editor" },
                { value: "viewer", label: "Viewer" },
              ]}
              value={inviteRole}
              onChange={(e) => setInviteRole(e.target.value)}
            />
            <Button
              onClick={handleGenerateInvite}
              loading={createInviteMutation.isPending}
            >
              Generate Link
            </Button>
          </div>
        ) : (
          <div className={styles.inviteResult}>
            <p className={styles.inviteResultLabel}>Share this link:</p>
            <div className={styles.inviteLinkRow}>
              <input
                type="text"
                readOnly
                value={generatedInviteUrl}
                className={styles.inviteLinkInput}
                onFocus={(e) => e.target.select()}
              />
              <Button size="sm" variant="secondary" onClick={handleCopyLink}>
                {copied ? "Copied!" : "Copy"}
              </Button>
            </div>
          </div>
        )}
      </Dialog>
    </div>
  );
}
