package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// SkillService manages per-bdb skills (markdown instructions for agents).
// Skills live in the per-bdb SQLite platform tables _skills and _skill_targets.
type SkillService struct {
	dbSvc    *DatabaseService
	executor queryExecutor
}

// NewSkillService creates a new SkillService.
func NewSkillService(dbSvc *DatabaseService, executor queryExecutor) *SkillService {
	return &SkillService{dbSvc: dbSvc, executor: executor}
}

// SkillInput is the payload for creating a new skill.
type SkillInput struct {
	Name         string
	Description  string
	Body         string
	TargetTables []string
}

// SkillPatch is the payload for partially updating a skill. Nil pointer means
// "leave unchanged"; non-nil means "replace with this value".
type SkillPatch struct {
	Name         *string
	Description  *string
	Body         *string
	TargetTables *[]string
}

// CreateSkill validates the input, generates a new UUID, and inserts the skill
// + its target tables atomically. Returns ErrSkillNameTaken if the name is in use.
func (s *SkillService) CreateSkill(
	ctx context.Context, userID, bdbID uuid.UUID, input SkillInput,
) (*domain.Skill, error) {
	ctx, span := tracer.Start(ctx, "SkillService.CreateSkill",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("skill.name", input.Name),
		),
	)
	defer span.End()

	if err := domain.ValidateSkillName(input.Name); err != nil {
		return nil, err
	}
	if err := domain.ValidateSkillDescription(input.Description); err != nil {
		return nil, err
	}
	if err := domain.ValidateSkillBody(input.Body); err != nil {
		return nil, err
	}
	targets := normalizeTargets(input.TargetTables)

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}

	if err := s.verifyTargetTablesExist(ctx, db.ID.String(), targets); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	skill := &domain.Skill{
		ID:           uuid.New(),
		Name:         input.Name,
		Description:  input.Description,
		Body:         input.Body,
		TargetTables: targets,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	stmts := []domain.SQLStatement{
		{
			SQL: fmt.Sprintf("INSERT INTO %s (id, name, description, body, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?)", domain.SkillsTable),
			Args: []domain.TypedValue{
				domain.TextValue(skill.ID.String()),
				domain.TextValue(skill.Name),
				domain.TextValue(skill.Description),
				domain.TextValue(skill.Body),
				domain.TextValue(domain.FormatSQLiteTimestamp(skill.CreatedAt)),
				domain.TextValue(domain.FormatSQLiteTimestamp(skill.UpdatedAt)),
			},
		},
	}
	for _, t := range targets {
		stmts = append(stmts, domain.SQLStatement{
			SQL: fmt.Sprintf("INSERT INTO %s (skill_id, table_name) VALUES (?, ?)", domain.SkillTargetsTable),
			Args: []domain.TypedValue{
				domain.TextValue(skill.ID.String()),
				domain.TextValue(t),
			},
		})
	}

	if _, err := s.executor.ExecuteTransactionInternal(ctx, db.ID.String(), stmts); err != nil {
		if errors.Is(err, domain.ErrUniqueViolation) {
			return nil, fmt.Errorf("%w: %s", domain.ErrSkillNameTaken, input.Name)
		}
		return nil, fmt.Errorf("creating skill: %w", err)
	}

	return skill, nil
}

// UpdateSkill applies a partial patch to an existing skill. The skill is
// resolved by name or UUID. Returns the fully-loaded updated skill.
func (s *SkillService) UpdateSkill(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, patch SkillPatch,
) (*domain.Skill, error) {
	ctx, span := tracer.Start(ctx, "SkillService.UpdateSkill",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("skill.ref", ref),
		),
	)
	defer span.End()

	if patch.Name != nil {
		if err := domain.ValidateSkillName(*patch.Name); err != nil {
			return nil, err
		}
	}
	if patch.Description != nil {
		if err := domain.ValidateSkillDescription(*patch.Description); err != nil {
			return nil, err
		}
	}
	if patch.Body != nil {
		if err := domain.ValidateSkillBody(*patch.Body); err != nil {
			return nil, err
		}
	}

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	existing, err := s.fetchSkillByRef(ctx, databaseID, ref)
	if err != nil {
		return nil, err
	}

	if patch.TargetTables != nil {
		if err := s.verifyTargetTablesExist(ctx, databaseID, normalizeTargets(*patch.TargetTables)); err != nil {
			return nil, err
		}
	}

	setClauses := []string{"updated_at = ?"}
	args := []domain.TypedValue{domain.TextValue(domain.FormatSQLiteTimestamp(time.Now()))}
	if patch.Name != nil {
		setClauses = append(setClauses, "name = ?")
		args = append(args, domain.TextValue(*patch.Name))
	}
	if patch.Description != nil {
		setClauses = append(setClauses, "description = ?")
		args = append(args, domain.TextValue(*patch.Description))
	}
	if patch.Body != nil {
		setClauses = append(setClauses, "body = ?")
		args = append(args, domain.TextValue(*patch.Body))
	}
	args = append(args, domain.TextValue(existing.ID.String()))

	stmts := []domain.SQLStatement{
		{
			SQL:  fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", domain.SkillsTable, strings.Join(setClauses, ", ")),
			Args: args,
		},
	}
	if patch.TargetTables != nil {
		// Replaces the full target set: delete all existing rows, then insert the new ones.
		stmts = append(stmts, domain.SQLStatement{
			SQL:  fmt.Sprintf("DELETE FROM %s WHERE skill_id = ?", domain.SkillTargetsTable),
			Args: []domain.TypedValue{domain.TextValue(existing.ID.String())},
		})
		for _, t := range normalizeTargets(*patch.TargetTables) {
			stmts = append(stmts, domain.SQLStatement{
				SQL: fmt.Sprintf("INSERT INTO %s (skill_id, table_name) VALUES (?, ?)", domain.SkillTargetsTable),
				Args: []domain.TypedValue{
					domain.TextValue(existing.ID.String()),
					domain.TextValue(t),
				},
			})
		}
	}

	if _, err := s.executor.ExecuteTransactionInternal(ctx, databaseID, stmts); err != nil {
		if errors.Is(err, domain.ErrUniqueViolation) {
			return nil, fmt.Errorf("%w", domain.ErrSkillNameTaken)
		}
		return nil, fmt.Errorf("updating skill: %w", err)
	}

	return s.fetchSkillByID(ctx, databaseID, existing.ID)
}

// GetSkill resolves a skill by name or UUID and returns it with its target tables.
func (s *SkillService) GetSkill(ctx context.Context, userID, bdbID uuid.UUID, ref string) (*domain.Skill, error) {
	ctx, span := tracer.Start(ctx, "SkillService.GetSkill",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("skill.ref", ref),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	return s.fetchSkillByRef(ctx, db.ID.String(), ref)
}

// ListSkills returns every skill in the bdb with its target tables, sorted by name.
func (s *SkillService) ListSkills(ctx context.Context, userID, bdbID uuid.UUID) ([]*domain.Skill, error) {
	ctx, span := tracer.Start(ctx, "SkillService.ListSkills",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	skills, err := s.fetchAllSkills(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return skills, nil
	}

	if err := s.attachTargets(ctx, databaseID, skills); err != nil {
		return nil, err
	}
	return skills, nil
}

// ListSkillsForTable returns every skill that targets the given table, sorted
// by name. Used by describe_table to surface the full skill bodies relevant
// to that table.
func (s *SkillService) ListSkillsForTable(
	ctx context.Context, userID, bdbID uuid.UUID, tableName string,
) ([]*domain.Skill, error) {
	ctx, span := tracer.Start(ctx, "SkillService.ListSkillsForTable",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("table.name", tableName),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf(`SELECT s.id, s.name, s.description, s.body, s.created_at, s.updated_at
FROM %s s
JOIN %s t ON t.skill_id = s.id
WHERE t.table_name = ?
ORDER BY s.name ASC`, domain.SkillsTable, domain.SkillTargetsTable),
		[]domain.TypedValue{domain.TextValue(tableName)},
	)
	if err != nil {
		return nil, fmt.Errorf("listing skills for table: %w", err)
	}
	if result == nil || len(result.Rows) == 0 {
		return []*domain.Skill{}, nil
	}

	skills := make([]*domain.Skill, 0, len(result.Rows))
	for _, row := range result.Rows {
		skill, err := scanSkillRow(row)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}

	if err := s.attachTargets(ctx, databaseID, skills); err != nil {
		return nil, err
	}
	return skills, nil
}

// DeleteSkill removes the skill (its target rows are removed via FK cascade).
// Returns ErrSkillNotFound if the skill does not exist.
func (s *SkillService) DeleteSkill(ctx context.Context, userID, bdbID uuid.UUID, ref string) error {
	ctx, span := tracer.Start(ctx, "SkillService.DeleteSkill",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("skill.ref", ref),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return err
	}
	databaseID := db.ID.String()

	existing, err := s.fetchSkillByRef(ctx, databaseID, ref)
	if err != nil {
		return err
	}

	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = ?", domain.SkillsTable)
	if _, err := s.executor.ExecuteInternal(ctx, databaseID, deleteSQL,
		[]domain.TypedValue{domain.TextValue(existing.ID.String())}); err != nil {
		return fmt.Errorf("deleting skill: %w", err)
	}
	return nil
}

// verifyTargetTablesExist checks that every name in targets corresponds to an
// existing user-created table or view in the bdb's SQLite database.
// Excludes platform tables (`_*` prefix) and SQLite internals (`sqlite_*`).
// Returns ErrSkillTargetNotFound listing the missing names if any are absent.
func (s *SkillService) verifyTargetTablesExist(ctx context.Context, databaseID string, targets []string) error {
	if len(targets) == 0 {
		return nil
	}

	placeholders := make([]string, len(targets))
	args := make([]domain.TypedValue, len(targets))
	for i, t := range targets {
		placeholders[i] = "?"
		args[i] = domain.TextValue(t)
	}

	sql := fmt.Sprintf(
		`SELECT name FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%%' `+
			`AND name NOT LIKE '\_%%' ESCAPE '\' AND name IN (%s)`,
		strings.Join(placeholders, ", "),
	)

	result, err := s.executor.ExecuteInternal(ctx, databaseID, sql, args)
	if err != nil {
		return fmt.Errorf("verifying target tables: %w", err)
	}

	found := make(map[string]struct{}, len(result.Rows))
	for _, row := range result.Rows {
		if name, ok := row[0].Value.(string); ok {
			found[name] = struct{}{}
		}
	}

	var missing []string
	for _, t := range targets {
		if _, ok := found[t]; !ok {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", domain.ErrSkillTargetNotFound, strings.Join(missing, ", "))
	}
	return nil
}

// CascadeStatementOnTableDropped returns the SQL that removes _skill_targets
// entries for tableName. Skills with no remaining targets are kept (orphan
// policy: never auto-delete).
func CascadeStatementOnTableDropped(tableName string) domain.SQLStatement {
	return domain.SQLStatement{
		SQL:  fmt.Sprintf("DELETE FROM %s WHERE table_name = ?", domain.SkillTargetsTable),
		Args: []domain.TypedValue{domain.TextValue(tableName)},
	}
}

// CascadeStatementOnTableRenamed returns the SQL that updates _skill_targets
// entries from oldName to newName.
func CascadeStatementOnTableRenamed(oldName, newName string) domain.SQLStatement {
	return domain.SQLStatement{
		SQL: fmt.Sprintf("UPDATE %s SET table_name = ? WHERE table_name = ?", domain.SkillTargetsTable),
		Args: []domain.TypedValue{
			domain.TextValue(newName),
			domain.TextValue(oldName),
		},
	}
}

// --- internal helpers ---

// fetchSkillByRef resolves a ref as either a UUID or a slug name and returns
// the matching skill (with its target tables loaded).
func (s *SkillService) fetchSkillByRef(ctx context.Context, databaseID, ref string) (*domain.Skill, error) {
	var (
		whereCol string
		arg      domain.TypedValue
	)
	if id, err := uuid.Parse(ref); err == nil {
		whereCol = "id"
		arg = domain.TextValue(id.String())
	} else {
		whereCol = "name"
		arg = domain.TextValue(ref)
	}

	row, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT id, name, description, body, created_at, updated_at FROM %s WHERE %s = ?",
			domain.SkillsTable, whereCol),
		[]domain.TypedValue{arg},
	)
	if err != nil {
		return nil, fmt.Errorf("fetching skill: %w", err)
	}
	if row == nil || len(row.Rows) == 0 {
		return nil, fmt.Errorf("%w: %s", domain.ErrSkillNotFound, ref)
	}

	skill, err := scanSkillRow(row.Rows[0])
	if err != nil {
		return nil, err
	}

	if err := s.attachTargets(ctx, databaseID, []*domain.Skill{skill}); err != nil {
		return nil, err
	}
	return skill, nil
}

// fetchSkillByID is the post-write reload helper that always uses the UUID.
func (s *SkillService) fetchSkillByID(ctx context.Context, databaseID string, id uuid.UUID) (*domain.Skill, error) {
	return s.fetchSkillByRef(ctx, databaseID, id.String())
}

// fetchAllSkills returns all skills (without targets) sorted by name.
func (s *SkillService) fetchAllSkills(ctx context.Context, databaseID string) ([]*domain.Skill, error) {
	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT id, name, description, body, created_at, updated_at FROM %s ORDER BY name ASC",
			domain.SkillsTable),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	if result == nil || len(result.Rows) == 0 {
		return []*domain.Skill{}, nil
	}

	skills := make([]*domain.Skill, 0, len(result.Rows))
	for _, row := range result.Rows {
		skill, err := scanSkillRow(row)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

// attachTargets populates TargetTables on each skill via a single SQL query.
func (s *SkillService) attachTargets(ctx context.Context, databaseID string, skills []*domain.Skill) error {
	if len(skills) == 0 {
		return nil
	}

	idIndex := make(map[string]*domain.Skill, len(skills))
	placeholders := make([]string, 0, len(skills))
	args := make([]domain.TypedValue, 0, len(skills))
	for _, skill := range skills {
		idIndex[skill.ID.String()] = skill
		placeholders = append(placeholders, "?")
		args = append(args, domain.TextValue(skill.ID.String()))
		// Ensures TargetTables is non-nil for callers expecting a slice.
		skill.TargetTables = []string{}
	}

	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT skill_id, table_name FROM %s WHERE skill_id IN (%s)", domain.SkillTargetsTable,
			strings.Join(placeholders, ", ")),
		args,
	)
	if err != nil {
		return fmt.Errorf("loading skill targets: %w", err)
	}
	if result == nil {
		return nil
	}

	for _, row := range result.Rows {
		if len(row) < 2 {
			continue
		}
		skillID, _ := row[0].Value.(string)
		tableName, _ := row[1].Value.(string)
		if skill, ok := idIndex[skillID]; ok {
			skill.TargetTables = append(skill.TargetTables, tableName)
		}
	}

	for _, skill := range skills {
		sort.Strings(skill.TargetTables)
	}
	return nil
}

// scanSkillRow maps a single result row from _skills into a *domain.Skill
// (without target tables — those are loaded separately).
func scanSkillRow(row []domain.TypedValue) (*domain.Skill, error) {
	if len(row) < 6 {
		return nil, fmt.Errorf("%w: skill row has %d columns, want 6", domain.ErrQueryExecution, len(row))
	}
	idStr, _ := row[0].Value.(string)
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid skill id %q", domain.ErrQueryExecution, idStr)
	}
	name, _ := row[1].Value.(string)
	description, _ := row[2].Value.(string)
	body, _ := row[3].Value.(string)
	return &domain.Skill{
		ID:           id,
		Name:         name,
		Description:  description,
		Body:         body,
		TargetTables: []string{},
		CreatedAt:    domain.ParseSQLiteTimestamp(row[4].String()),
		UpdatedAt:    domain.ParseSQLiteTimestamp(row[5].String()),
	}, nil
}

// normalizeTargets trims whitespace, drops empties, and de-duplicates target
// table names while preserving sorted output for stable comparisons.
func normalizeTargets(targets []string) []string {
	if len(targets) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
