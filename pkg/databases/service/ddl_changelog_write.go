package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// insertChangeLogDDL writes one DDL row to _change_log for the unit shaped by stmt and alterInfo, under batchID with
// origin (ChangeOriginChange when empty). Returns the row's auto-generated id, used to key the optional
// _change_snapshots sidecar row. For ALTER TABLE statements, metadata carries the subtype-specific shape (column
// name, new table name) the revert generator needs.
//
// ObjectName conventions:
//   - CREATE TABLE / DROP TABLE: the table's name.
//   - CREATE / DROP INDEX, VIEW, TRIGGER: the object's own name, never the table it attaches to.
//   - ALTER TABLE (all subtypes): the name the table has when the statement runs. For RENAME TO the new name is in
//     DDLMetadata.NewTableName, so a rename back is recorded against the renamed table.
func insertChangeLogDDL(
	ctx context.Context,
	tx Tx,
	batchID string,
	userID uuid.UUID,
	stmt domain.ClassifiedStatement,
	alterInfo domain.AlterTableInfo,
	metadata *domain.DDLAlterMetadata,
	origin string,
) (int64, error) {
	if origin == "" {
		origin = domain.ChangeOriginChange
	}
	operation, objectType := ddlTypeToChangeLog(stmt.DDLType)
	if operation == "" {
		return 0, fmt.Errorf("insertChangeLogDDL: unsupported DDLType %v", stmt.DDLType)
	}

	objectName := ""
	if len(stmt.Tables) > 0 {
		objectName = stmt.Tables[0]
	}
	if stmt.DDLType == domain.DDLAlterTable && alterInfo.TableName != "" {
		// AlterTableInfo.TableName is always the original (pre-ALTER)
		// table name regardless of subtype.
		objectName = alterInfo.TableName
	}

	ddlSubtype := ""
	if stmt.DDLType == domain.DDLAlterTable {
		ddlSubtype = alterInfo.Subtype.String()
	}

	metadataArg := domain.NullValue()
	if metadata != nil {
		blob, err := json.Marshal(metadata)
		if err != nil {
			return 0, fmt.Errorf("marshal ddl_metadata: %w", err)
		}
		metadataArg = domain.TextValue(string(blob))
	}

	const insertSQL = `INSERT INTO _change_log
		(batch_id, kind, operation, origin, changed_by,
		 object_type, object_name, ddl_subtype, ddl_statement, ddl_metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	args := []domain.TypedValue{
		domain.TextValue(batchID),
		domain.TextValue(string(domain.ChangeKindDDL)),
		domain.TextValue(operation),
		domain.TextValue(origin),
		domain.TextValue(userID.String()),
		domain.TextValue(objectType),
		domain.TextValue(objectName),
		nullableText(ddlSubtype),
		domain.TextValue(stmt.SQL),
		metadataArg,
	}

	r, err := tx.ExecInternal(ctx, insertSQL, args)
	if err != nil {
		return 0, fmt.Errorf("insert _change_log: %w", err)
	}
	if r.LastInsertRowID == nil {
		return 0, errors.New("insert _change_log: no LastInsertRowID")
	}
	return *r.LastInsertRowID, nil
}

// insertChangeSnapshot writes the sidecar pre-image row for a destructive
// DDL change. column_metadata / dependent_objects / row_payload are
// JSON-encoded; FK ON DELETE CASCADE on change_log_id retires the row
// when its parent _change_log entry is pruned by retention.
func insertChangeSnapshot(
	ctx context.Context,
	tx Tx,
	changeLogID int64,
	snap domain.ChangeSnapshot,
) error {
	colMetaArg, err := jsonOrNull(snap.ColumnMetadata)
	if err != nil {
		return fmt.Errorf("marshal column_metadata: %w", err)
	}
	depsArg, err := jsonOrNull(snap.DependentObjects)
	if err != nil {
		return fmt.Errorf("marshal dependent_objects: %w", err)
	}
	payloadArg, err := jsonOrNull(snap.RowPayload)
	if err != nil {
		return fmt.Errorf("marshal row_payload: %w", err)
	}

	truncated := int64(0)
	if snap.Truncated {
		truncated = 1
	}

	const insertSQL = `INSERT INTO _change_snapshots
		(change_log_id, column_metadata, dependent_objects, row_payload, row_count_total, truncated)
		VALUES (?, ?, ?, ?, ?, ?)`

	args := []domain.TypedValue{
		domain.IntegerValue(changeLogID),
		colMetaArg,
		depsArg,
		payloadArg,
		domain.IntegerValue(int64(snap.RowCountTotal)),
		domain.IntegerValue(truncated),
	}
	if _, err := tx.ExecInternal(ctx, insertSQL, args); err != nil {
		return fmt.Errorf("insert _change_snapshots: %w", err)
	}
	return nil
}

// ddlTypeToChangeLog maps the classifier's DDLType to the operation and
// object_type strings stored in _change_log.
func ddlTypeToChangeLog(ddlType domain.DDLType) (operation, objectType string) {
	switch ddlType {
	case domain.DDLCreateTable:
		return domain.ChangeOpCreate, domain.ChangeObjectTable
	case domain.DDLDropTable:
		return domain.ChangeOpDrop, domain.ChangeObjectTable
	case domain.DDLAlterTable:
		return domain.ChangeOpAlter, domain.ChangeObjectTable
	case domain.DDLCreateIndex:
		return domain.ChangeOpCreate, domain.ChangeObjectIndex
	case domain.DDLDropIndex:
		return domain.ChangeOpDrop, domain.ChangeObjectIndex
	case domain.DDLCreateView:
		return domain.ChangeOpCreate, domain.ChangeObjectView
	case domain.DDLDropView:
		return domain.ChangeOpDrop, domain.ChangeObjectView
	case domain.DDLCreateTrigger:
		return domain.ChangeOpCreate, domain.ChangeObjectTrigger
	case domain.DDLDropTrigger:
		return domain.ChangeOpDrop, domain.ChangeObjectTrigger
	default:
		return "", ""
	}
}

// nullableText returns NullValue for empty strings and TextValue otherwise.
func nullableText(s string) domain.TypedValue {
	if s == "" {
		return domain.NullValue()
	}
	return domain.TextValue(s)
}

// jsonOrNull marshals v as JSON and returns the TypedValue to bind. nil
// inputs and zero-length results map to SQL NULL so the sidecar row's
// columns aren't padded with empty-JSON strings.
func jsonOrNull(v any) (domain.TypedValue, error) {
	if v == nil {
		return domain.NullValue(), nil
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return domain.NullValue(), err
	}
	s := string(blob)
	if s == "" || s == "null" {
		return domain.NullValue(), nil
	}
	return domain.TextValue(s), nil
}
