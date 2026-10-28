//go:build integration

package integrationtest

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyWorkouts applies the embedded workouts starter for a fresh user and returns the wired services with
// the new bdb's coordinates, ready for formula runs.
func applyWorkouts(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "workouts", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(workouts) error = %v", err)
	}
	return deps, userID, bdbID
}

// canonicalDatetime is the stored shape of a session timestamp — the form SQLite's datetime() writes.
var canonicalDatetime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

// TestWorkoutsAddSessionStampsStart pins the quick-add contract: a session added with only its kind gets
// started_at stamped with the current moment (an unstamped session would sink undated to the bottom of the
// feed), and the card title carries the day.
func TestWorkoutsAddSessionStampsStart(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})

	stored := execSQL(t, deps, userID, bdbID, "SELECT started_at FROM sessions", dbdomain.IntentQuery)
	startedAt, _ := cell(t, stored, 0, "started_at").(string)
	if !canonicalDatetime.MatchString(startedAt) {
		t.Errorf("started_at = %q, want the current moment stamped in canonical datetime form", startedAt)
	}

	feed := runFormula(t, deps, userID, bdbID, "workouts", nil)
	if title, _ := cell(t, feed.Result, 0, "title").(string); !strings.HasPrefix(title, "Gym — ") {
		t.Errorf("card title = %q, want the kind with the stamped day", title)
	}
}

// TestWorkoutsSessionTimingRoundTrip drives explicit timing through add and edit: picker-shaped
// "YYYY-MM-DDTHH:MM" inputs land stored in canonical "YYYY-MM-DD HH:MM:SS" form, the feed projects the raw
// started_at and ended_at so the edit dialog seeds what is stored, and an edit passing the seed back with a
// corrected end updates just that.
func TestWorkoutsSessionTimingRoundTrip(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{
		"kind": "run", "started_at": "2026-08-04T07:00", "ended_at": "2026-08-04T08:05",
	})

	stored := execSQL(t, deps, userID, bdbID, "SELECT started_at, ended_at FROM sessions", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "started_at"); v != "2026-08-04 07:00:00" {
		t.Errorf("started_at = %v, want the picker input canonicalized", v)
	}
	if v := cell(t, stored, 0, "ended_at"); v != "2026-08-04 08:05:00" {
		t.Errorf("ended_at = %v, want the picker input canonicalized", v)
	}

	feed := runFormula(t, deps, userID, bdbID, "workouts", nil)
	if v := cell(t, feed.Result, 0, "started_at"); v != "2026-08-04 07:00:00" {
		t.Errorf("feed started_at = %v, want the raw stored value for the edit seed", v)
	}
	if v := cell(t, feed.Result, 0, "ended_at"); v != "2026-08-04 08:05:00" {
		t.Errorf("feed ended_at = %v, want the raw stored value for the edit seed", v)
	}

	rowid := float64(cell(t, feed.Result, 0, "rowid").(int64))
	runFormula(t, deps, userID, bdbID, "edit-session", map[string]any{
		"rowid": rowid, "kind": "run", "started_at": "2026-08-04 07:00:00", "ended_at": "2026-08-04T08:10",
	})
	stored = execSQL(t, deps, userID, bdbID, "SELECT started_at, ended_at FROM sessions", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "started_at"); v != "2026-08-04 07:00:00" {
		t.Errorf("started_at after edit = %v, want the seeded value accepted as-is", v)
	}
	if v := cell(t, stored, 0, "ended_at"); v != "2026-08-04 08:10:00" {
		t.Errorf("ended_at after edit = %v, want the correction", v)
	}
}

// TestWorkoutsKindOptions pins the kind vocabulary and its default: a value outside {gym, run} is refused at
// the boundary before reaching the CHECK constraint, a case variant canonicalizes to the option as written,
// and an omitted kind binds the gym default — the feed's one-tap quick add.
func TestWorkoutsKindOptions(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-session",
		dbservice.FormulaRunInput{Params: map[string]any{"kind": "yoga"}})
	if !errors.Is(err, dbdomain.ErrFormulaParamValue) {
		t.Errorf("RunFormula(kind=yoga) error = %v, want ErrFormulaParamValue", err)
	}

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "Gym"})
	stored := execSQL(t, deps, userID, bdbID, "SELECT kind FROM sessions", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "kind"); v != "gym" {
		t.Errorf("kind = %v, want the case variant canonicalized to the option", v)
	}

	runFormula(t, deps, userID, bdbID, "add-session", nil)
	stored = execSQL(t, deps, userID, bdbID,
		"SELECT kind FROM sessions ORDER BY id DESC LIMIT 1", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "kind"); v != "gym" {
		t.Errorf("omitted kind = %v, want the gym default bound", v)
	}
}

// TestWorkoutsExerciseListStats pins the catalog view's composed stats block across its three shapes — full
// (one line each for catalog info, the last session's day and sets, the PR with the day it was first reached,
// and the session count), partial (weightless exercise: no PR line, bare-rep last-session sets), and never-done
// (empty block) — plus the raw columns the edit dialog seeds from and the name/muscle-group search filter.
func TestWorkoutsExerciseListStats(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-exercise",
		map[string]any{"name": "Bench press", "muscle_group": "chest", "equipment": "barbell"})
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Pull-up", "muscle_group": "back"})
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Squat"})

	runFormula(t, deps, userID, bdbID, "add-session",
		map[string]any{"kind": "gym", "started_at": "2026-07-14T10:00"})
	runFormula(t, deps, userID, bdbID, "add-session",
		map[string]any{"kind": "gym", "started_at": "2026-08-02T18:00"})
	sessions := execSQL(t, deps, userID, bdbID, "SELECT id FROM sessions ORDER BY started_at", dbdomain.IntentQuery)
	first := float64(cell(t, sessions, 0, "id").(int64))
	second := float64(cell(t, sessions, 1, "id").(int64))
	bench := "Bench press"
	logSet := func(parent float64, exercise string, reps int, weight any) {
		t.Helper()
		params := map[string]any{"parent_rowid": parent, "exercise": exercise, "reps": float64(reps)}
		if weight != nil {
			params["weight"] = weight
		}
		runFormula(t, deps, userID, bdbID, "log-set", params)
	}
	logSet(first, bench, 5, 80.0)
	logSet(first, bench, 5, 82.5)
	logSet(second, bench, 6, 80.0)
	logSet(second, "Pull-up", 10, nil)

	list := runFormula(t, deps, userID, bdbID, "exercise-list", nil)
	if len(list.Result.Rows) != 3 {
		t.Fatalf("exercise rows = %d, want 3", len(list.Result.Rows))
	}
	byName := map[string]string{}
	for i := range list.Result.Rows {
		name, _ := cell(t, list.Result, i, "name").(string)
		stats, _ := cell(t, list.Result, i, "display_stats").(string)
		byName[name] = stats
	}
	want := "chest · barbell\n2026-08-02 — 80kg×6\nPR 82.5 kg (2026-07-14)\n2 sessions"
	if byName[bench] != want {
		t.Errorf("bench stats = %q, want %q", byName[bench], want)
	}
	if byName["Pull-up"] != "back\n2026-08-02 — 10\n1 session" {
		t.Errorf("pull-up stats = %q, want the PR line absent for a weightless exercise", byName["Pull-up"])
	}
	if byName["Squat"] != "" {
		t.Errorf("squat stats = %q, want empty for a never-done exercise", byName["Squat"])
	}
	for i := range list.Result.Rows {
		if name := cell(t, list.Result, i, "name"); name == bench {
			if mg := cell(t, list.Result, i, "muscle_group"); mg != "chest" {
				t.Errorf("raw muscle_group = %v, want the stored value for the edit seed", mg)
			}
			if eq := cell(t, list.Result, i, "equipment"); eq != "barbell" {
				t.Errorf("raw equipment = %v, want the stored value for the edit seed", eq)
			}
		}
	}

	if hits := runFormula(t, deps, userID, bdbID, "exercise-list",
		map[string]any{"query": "bench"}); len(hits.Result.Rows) != 1 {
		t.Errorf("query=bench rows = %d, want only the matching exercise", len(hits.Result.Rows))
	}
	if hits := runFormula(t, deps, userID, bdbID, "exercise-list",
		map[string]any{"query": "back"}); len(hits.Result.Rows) != 1 {
		t.Errorf("query=back rows = %d, want the muscle-group match", len(hits.Result.Rows))
	}
	if blank := runFormula(t, deps, userID, bdbID, "exercise-list",
		map[string]any{"query": "  "}); len(blank.Result.Rows) != 3 {
		t.Errorf("blank-query rows = %d, want everything rather than nothing", len(blank.Result.Rows))
	}
}

// TestWorkoutsExerciseDetailHistory pins the tap-through history: one row per session the exercise
// appeared in, newest first, each with the day and that session's set breakdown (repeated sets compressed,
// only that session's sets, weightless sets as bare rep counts).
func TestWorkoutsExerciseDetailHistory(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-exercise",
		map[string]any{"name": "Deadlift", "muscle_group": "back", "equipment": "barbell"})
	runFormula(t, deps, userID, bdbID, "add-session",
		map[string]any{"kind": "gym", "started_at": "2026-07-01T09:00"})
	runFormula(t, deps, userID, bdbID, "add-session",
		map[string]any{"kind": "gym", "started_at": "2026-07-20T09:00"})
	sessions := execSQL(t, deps, userID, bdbID, "SELECT id FROM sessions ORDER BY started_at", dbdomain.IntentQuery)
	exercise := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises", dbdomain.IntentQuery), 0, "id").(int64))
	for _, set := range []struct {
		session int
		reps    float64
		weight  float64
	}{{0, 5, 100}, {0, 5, 100}, {0, 5, 110}, {1, 3, 120}} {
		runFormula(t, deps, userID, bdbID, "log-set", map[string]any{
			"parent_rowid": float64(cell(t, sessions, set.session, "id").(int64)),
			"exercise":     "Deadlift", "reps": set.reps, "weight": set.weight,
		})
	}
	// Two identical bodyweight sets in the newest session: they compress like weighted ones but render
	// as the bare rep count, no leading ×.
	for range 2 {
		runFormula(t, deps, userID, bdbID, "log-set", map[string]any{
			"parent_rowid": float64(cell(t, sessions, 1, "id").(int64)), "exercise": "Deadlift", "reps": 12.0,
		})
	}

	history := runFormula(t, deps, userID, bdbID, "exercise-detail", map[string]any{"rowid": exercise})
	if len(history.Result.Rows) != 2 {
		t.Fatalf("history rows = %d, want one per session", len(history.Result.Rows))
	}
	if day := cell(t, history.Result, 0, "day"); day != "2026-07-20" {
		t.Errorf("first history day = %v, want the newest session first", day)
	}
	if sets := cell(t, history.Result, 0, "sets"); sets != "120kg×3, 12 ×2" {
		t.Errorf("newest session sets = %v, want its sets with bare-rep bodyweight runs", sets)
	}
	if sets := cell(t, history.Result, 1, "sets"); sets != "100kg×5 ×2, 110kg×5" {
		t.Errorf("older session sets = %v, want its compressed breakdown", sets)
	}
}

// TestWorkoutsEditExercise pins the catalog edit round trip: renaming and filling in the catalog fields
// persists, seeded from the list view's raw columns.
func TestWorkoutsEditExercise(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bnch press"})
	rowid := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises", dbdomain.IntentQuery), 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "edit-exercise", map[string]any{
		"rowid": rowid, "name": "Bench press", "muscle_group": "chest", "equipment": "barbell",
	})
	stored := execSQL(t, deps, userID, bdbID,
		"SELECT name, muscle_group, equipment FROM exercises", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "name"); v != "Bench press" {
		t.Errorf("name = %v, want the corrected spelling", v)
	}
	if v := cell(t, stored, 0, "muscle_group"); v != "chest" {
		t.Errorf("muscle_group = %v, want it filled in", v)
	}
	if v := cell(t, stored, 0, "equipment"); v != "barbell" {
		t.Errorf("equipment = %v, want it filled in", v)
	}
}

// TestWorkoutsMergeExercise pins the duplicate fold: the duplicate's logged sets and program prescriptions
// move to the surviving exercise before the duplicate row is deleted, merging an exercise into itself is a
// no-op, and a duplicate with no children at all is simply removed.
func TestWorkoutsMergeExercise(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	// The duplicate picker must not offer the surviving row itself: the manifest's exclude_selection
	// flag has to survive the apply pipeline into the stored formula the app reads.
	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	for _, f := range formulas {
		if f.Name != "merge-exercise" {
			continue
		}
		for _, p := range f.Params {
			if p.Name == "duplicate_id" && (p.Lookup == nil || !p.Lookup.ExcludeSelection) {
				t.Errorf("merge-exercise duplicate_id lookup = %+v, want ExcludeSelection set", p.Lookup)
			}
		}
	}

	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bench press"})
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bench pres"})
	bench := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises WHERE name = 'Bench press'", dbdomain.IntentQuery), 0, "id").(int64))
	dupe := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises WHERE name = 'Bench pres'", dbdomain.IntentQuery), 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})
	session := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM sessions", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "log-set", map[string]any{
		"parent_rowid": session, "exercise": "Bench press", "reps": 5.0, "weight": 100.0,
	})
	runFormula(t, deps, userID, bdbID, "log-set", map[string]any{
		"parent_rowid": session, "exercise": "Bench pres", "reps": 8.0, "weight": 60.0,
	})
	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A",
	})
	program := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
		"parent_rowid": program, "exercise": "Bench pres",
	})

	runFormula(t, deps, userID, bdbID, "merge-exercise", map[string]any{"rowid": bench, "duplicate_id": dupe})

	catalog := execSQL(t, deps, userID, bdbID, "SELECT name FROM exercises", dbdomain.IntentQuery)
	if len(catalog.Rows) != 1 {
		t.Fatalf("exercises after merge = %d rows, want only the survivor", len(catalog.Rows))
	}
	if v := cell(t, catalog, 0, "name"); v != "Bench press" {
		t.Errorf("surviving exercise = %v, want the row the merge was run from", v)
	}
	moved := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM exercise_sets WHERE exercise_id = "+fmt.Sprintf("%.0f", bench),
		dbdomain.IntentQuery)
	if v := cell(t, moved, 0, "n"); v != int64(2) {
		t.Errorf("survivor's sets after merge = %v, want both its own and the duplicate's", v)
	}
	prescriptions := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM program_exercises WHERE exercise_id = "+fmt.Sprintf("%.0f", bench),
		dbdomain.IntentQuery)
	if v := cell(t, prescriptions, 0, "n"); v != int64(1) {
		t.Errorf("survivor's prescriptions after merge = %v, want the duplicate's moved over", v)
	}

	// Merging the survivor into itself must not delete it: the guards turn a self-merge into a no-op.
	runFormula(t, deps, userID, bdbID, "merge-exercise", map[string]any{"rowid": bench, "duplicate_id": bench})
	after := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM exercises", dbdomain.IntentQuery)
	if v := cell(t, after, 0, "n"); v != int64(1) {
		t.Fatalf("exercises after self-merge = %v, want the survivor untouched", v)
	}

	// A duplicate with no sets or prescriptions has nothing to move — the merge just removes it.
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bench presss"})
	junk := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises WHERE name = 'Bench presss'", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "merge-exercise", map[string]any{"rowid": bench, "duplicate_id": junk})
	cleaned := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM exercises", dbdomain.IntentQuery)
	if v := cell(t, cleaned, 0, "n"); v != int64(1) {
		t.Errorf("exercises after merging an unused duplicate = %v, want it simply removed", v)
	}
}

// TestWorkoutsProgramAuthoring pins the plan drill: a program's detail lists its planned sessions with a
// composed plan line (rep-less prescriptions render without a rep target) and the raw name/notes seed
// columns, the prescription checklist sits one level further behind the nested detail slot, prescriptions
// default to 3 sets when no count is given, and targets edit in place.
func TestWorkoutsProgramAuthoring(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bench press"})
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Overhead press"})
	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A", "notes": "Focus on tempo",
	})
	program := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
		"parent_rowid": program, "exercise": "Bench press", "target_sets": 3.0, "target_reps": 5.0,
	})
	runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
		"parent_rowid": program, "exercise": "Overhead press",
	})

	if v := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT target_sets FROM program_exercises WHERE exercise_id = "+
			"(SELECT id FROM exercises WHERE name = 'Overhead press')",
		dbdomain.IntentQuery), 0, "target_sets"); v != int64(3) {
		t.Errorf("omitted target_sets = %v, want the 3-set default", v)
	}

	list := runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if v := cell(t, list.Result, 0, "display_body"); v != "Bench press 3×5 · Overhead press ×3\nFocus on tempo" {
		t.Errorf("card body = %v, want the plan line with the notes line under it", v)
	}
	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM program_runs", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("program_runs rows = %v, want none — a gym plan carries no run targets", cell(t, n, 0, "n"))
	}
	if v := cell(t, list.Result, 0, "name"); v != "Push day A" {
		t.Errorf("raw name = %v, want the stored value for the edit seed", v)
	}

	detail := runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	if len(detail.Result.Rows) != 2 {
		t.Fatalf("prescription rows = %d, want 2", len(detail.Result.Rows))
	}
	if v := cell(t, detail.Result, 0, "line"); v != "1: Bench press — 3×5" {
		t.Errorf("prescription line = %v, want the position-prefixed exercise with its targets", v)
	}
	if v := cell(t, detail.Result, 1, "line"); v != "2: Overhead press — 3 sets" {
		t.Errorf("rep-less prescription line = %v, want the set count alone", v)
	}

	prescription := float64(cell(t, detail.Result, 1, "rowid").(int64))
	runFormula(t, deps, userID, bdbID, "edit-program-exercise", map[string]any{
		"rowid": prescription, "target_sets": 4.0, "target_reps": 8.0,
	})
	detail = runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	if v := cell(t, detail.Result, 1, "line"); v != "2: Overhead press — 4×8" {
		t.Errorf("edited prescription line = %v, want the corrected targets at the kept position", v)
	}
}

// TestWorkoutsProgramStartToday pins the start action end to end: a real gym session appears stamped with
// the current moment and named after the plan, carrying one skeleton set per prescribed set — weight and
// reps NULL, set numbers counting per exercise — and the feed's card summarizes the skeleton on top.
func TestWorkoutsProgramStartToday(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Bench press"})
	runFormula(t, deps, userID, bdbID, "add-exercise", map[string]any{"name": "Overhead press"})
	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A", "notes": "slow eccentric",
	})
	program := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	ohp := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM exercises WHERE name = 'Overhead press'", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
		"parent_rowid": program, "exercise": "Bench press", "target_sets": 3.0, "target_reps": 5.0,
	})
	runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
		"parent_rowid": program, "exercise": "Overhead press", "target_sets": 2.0,
	})
	// A pre-existing session must not receive the skeleton — the start targets the session it creates.
	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})

	runFormula(t, deps, userID, bdbID, "start-program-session", map[string]any{"rowid": program})

	session := execSQL(t, deps, userID, bdbID,
		"SELECT id, kind, started_at, notes, program_session_id FROM sessions ORDER BY id DESC LIMIT 1",
		dbdomain.IntentQuery)
	if v := cell(t, session, 0, "kind"); v != "gym" {
		t.Errorf("started session kind = %v, want gym", v)
	}
	if v, _ := cell(t, session, 0, "started_at").(string); !canonicalDatetime.MatchString(v) {
		t.Errorf("started session started_at = %q, want the current moment stamped", v)
	}
	if v := cell(t, session, 0, "notes"); v != "Push day A — slow eccentric" {
		t.Errorf("started session notes = %v, want the plan's name with its notes appended", v)
	}
	if v := cell(t, session, 0, "program_session_id"); v != int64(program) {
		t.Errorf("started session program_session_id = %v, want the planned session stamped", v)
	}

	day, _ := cell(t, execSQL(t, deps, userID, bdbID, "SELECT date('now') AS d", dbdomain.IntentQuery),
		0, "d").(string)
	list := runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if v, _ := cell(t, list.Result, 0, "display_body").(string); !strings.HasSuffix(v, "\nLast done "+day) {
		t.Errorf("planned-session card body = %q, want a last-done line dated today under the plan", v)
	}

	sessionID := cell(t, session, 0, "id").(int64)
	skeleton := execSQL(t, deps, userID, bdbID, fmt.Sprintf(
		"SELECT exercise_id, set_number, reps, weight FROM exercise_sets WHERE session_id = %d ORDER BY id",
		sessionID), dbdomain.IntentQuery)
	if len(skeleton.Rows) != 5 {
		t.Fatalf("skeleton rows = %d, want 3 bench + 2 overhead", len(skeleton.Rows))
	}
	for i := range skeleton.Rows {
		if reps := cell(t, skeleton, i, "reps"); reps != nil {
			t.Errorf("skeleton row %d reps = %v, want NULL — nothing happened yet", i, reps)
		}
		if weight := cell(t, skeleton, i, "weight"); weight != nil {
			t.Errorf("skeleton row %d weight = %v, want NULL — nothing happened yet", i, weight)
		}
	}
	if n := cell(t, skeleton, 2, "set_number"); n != int64(3) {
		t.Errorf("third bench skeleton set_number = %v, want positions running through the session", n)
	}
	if ex, n := cell(t, skeleton, 3, "exercise_id"), cell(t, skeleton, 3, "set_number"); ex != int64(ohp) ||
		n != int64(4) {
		t.Errorf("fourth skeleton = exercise %v at position %v, want the second prescription's exercise"+
			" carrying on from the first's positions rather than restarting at 1", ex, n)
	}
	if n := execSQL(t, deps, userID, bdbID, fmt.Sprintf(
		"SELECT COUNT(*) AS n FROM exercise_sets WHERE session_id <> %d", sessionID),
		dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("pre-existing session gained sets, want the skeleton only on the started session")
	}
	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM runs", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("runs rows = %v, want none — starting a gym plan seeds no run", cell(t, n, 0, "n"))
	}

	feed := runFormula(t, deps, userID, bdbID, "workouts", nil)
	if v := cell(t, feed.Result, 0, "summary"); v != "Bench press: ? ×3; Overhead press: ? ×2" {
		t.Errorf("feed summary = %v, want the skeleton compressed on the newest card", v)
	}
}

// TestWorkoutsProgramStartRun pins the planned-run path: a kind='run' planned session stores its targets in
// program_runs (target duration deriving from pace and distance), renders them on its card and seeds them to
// the edit dialog, re-derives on edit, and starting it creates a run session with its empty runs row — no
// skeleton sets, no targets copied — listed as a stub in the Runs tab with its provenance stamped.
func TestWorkoutsProgramStartRun(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Easy 10k", "kind": "run", "notes": "10 km easy",
		"target_distance_km": 10.0, "target_pace": "5:30",
	})

	targets := execSQL(t, deps, userID, bdbID,
		"SELECT target_distance_km, target_pace_seconds_per_km, target_duration_minutes FROM program_runs",
		dbdomain.IntentQuery)
	if len(targets.Rows) != 1 || cell(t, targets, 0, "target_pace_seconds_per_km") != int64(330) ||
		cell(t, targets, 0, "target_duration_minutes") != int64(55) {
		t.Fatalf("program_runs = %v, want pace 330 with the 55 min duration derived", targets.Rows)
	}

	list := runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if v := cell(t, list.Result, 0, "display_body"); v != "Run — target 10 km · 5:30 /km · 55 min\n10 km easy" {
		t.Errorf("planned run body = %v, want the target line with the notes under it", v)
	}
	if v := cell(t, list.Result, 0, "target_pace"); v != "5:30" {
		t.Errorf("target pace seed = %v, want the stored seconds formatted back to M:SS", v)
	}

	// Editing with a duration and no pace re-derives the pace; the kind keeps its stored value when cleared.
	planned := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "edit-program-session", map[string]any{
		"rowid": planned, "name": "Easy 10k", "notes": "10 km easy",
		"target_distance_km": 10.0, "target_duration_minutes": 50.0,
	})
	targets = execSQL(t, deps, userID, bdbID,
		"SELECT target_pace_seconds_per_km, target_duration_minutes FROM program_runs", dbdomain.IntentQuery)
	if cell(t, targets, 0, "target_pace_seconds_per_km") != int64(300) ||
		cell(t, targets, 0, "target_duration_minutes") != int64(50) {
		t.Fatalf("edited program_runs = %v, want the 300 s/km pace derived from 50 min over 10 km", targets.Rows)
	}

	runFormula(t, deps, userID, bdbID, "start-program-session", map[string]any{"rowid": planned})

	session := execSQL(t, deps, userID, bdbID,
		"SELECT id, kind, notes, program_session_id FROM sessions", dbdomain.IntentQuery)
	if len(session.Rows) != 1 || cell(t, session, 0, "kind") != "run" {
		t.Fatalf("started sessions = %v, want a single run session", session.Rows)
	}
	// The targets and the plan's notes ride along as prose — the intent stays in sight while the
	// actual stats are filled in.
	if v := cell(t, session, 0, "notes"); v != "Easy 10k — target 10 km · 5:00 /km · 50 min — 10 km easy" {
		t.Errorf("started run notes = %v, want the plan's name with its target line and notes", v)
	}
	if v := cell(t, session, 0, "program_session_id"); v != int64(planned) {
		t.Errorf("started run program_session_id = %v, want the planned session stamped", v)
	}
	paired := execSQL(t, deps, userID, bdbID,
		"SELECT session_id, distance_km FROM runs", dbdomain.IntentQuery)
	if len(paired.Rows) != 1 || cell(t, paired, 0, "session_id") != cell(t, session, 0, "id") {
		t.Fatalf("runs rows = %v, want the empty run row paired with the started session", paired.Rows)
	}
	if v := cell(t, paired, 0, "distance_km"); v != nil {
		t.Errorf("seeded run distance = %v, want NULL — targets aren't results", v)
	}
	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM exercise_sets", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("skeleton sets = %v, want none for a planned run", cell(t, n, 0, "n"))
	}
	runs := runFormula(t, deps, userID, bdbID, "run-list", nil)
	if len(runs.Result.Rows) != 1 {
		t.Fatalf("Runs tab rows = %d, want the started run listed as a stub", len(runs.Result.Rows))
	}
}

// TestWorkoutsProgramsDrill pins the single-tab plan surface: the Programs cards compose their status line
// (rotation badge, session count, notes), each program's detail scopes to its own planned sessions —
// including out-of-rotation programs, which stay fully browsable — and toggle-program flips only the badge.
func TestWorkoutsProgramsDrill(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A",
	})
	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block B", "notes": "deload"})
	blockB := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs WHERE name = 'Block B'", dbdomain.IntentQuery), 0, "id").(int64))

	// Both programs are in rotation from creation.
	list := runFormula(t, deps, userID, bdbID, "programs-list", nil)
	if len(list.Result.Rows) != 2 {
		t.Fatalf("programs = %d, want 2", len(list.Result.Rows))
	}
	if v := cell(t, list.Result, 0, "display_body"); v != "Active · 1 planned session" {
		t.Errorf("first program body = %v, want the marker with a singular session count", v)
	}
	if v := cell(t, list.Result, 1, "display_body"); v != "Active · 0 planned sessions\ndeload" {
		t.Errorf("second program body = %v, want the plural zero count with the notes line", v)
	}

	// Each program's detail lists its own planned sessions, nothing of its siblings.
	detail := runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if len(detail.Result.Rows) != 1 || cell(t, detail.Result, 0, "name") != "Push day A" {
		t.Fatalf("Block A detail rows = %v, want its planned session alone", detail.Result.Rows)
	}
	if detail = runFormula(t, deps, userID, bdbID, "program-detail",
		map[string]any{"rowid": blockB}); len(detail.Result.Rows) != 0 {
		t.Fatalf("Block B detail rows = %d, want the fresh block empty", len(detail.Result.Rows))
	}

	// The add binds the program its card is open for.
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockB, "name": "Pull day",
	})
	detail = runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockB})
	if len(detail.Result.Rows) != 1 || cell(t, detail.Result, 0, "name") != "Pull day" {
		t.Fatalf("Block B detail rows = %v, want the session added from its card", detail.Result.Rows)
	}

	// Toggling out of rotation flips the badge only — the program stays fully browsable.
	runFormula(t, deps, userID, bdbID, "toggle-program", map[string]any{"rowid": blockA})
	list = runFormula(t, deps, userID, bdbID, "programs-list", nil)
	if v := cell(t, list.Result, 0, "name"); v != "Block B" {
		t.Errorf("first program card = %v, want the remaining rotation sorted first", v)
	}
	if v := cell(t, list.Result, 1, "display_body"); v != "1 planned session" {
		t.Errorf("toggled-off program body = %v, want the badge gone and the count kept", v)
	}
	detail = runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if len(detail.Result.Rows) != 1 || cell(t, detail.Result, 0, "name") != "Push day A" {
		t.Fatalf("toggled-off program detail rows = %v, want it still browsable", detail.Result.Rows)
	}

	// The drill is wired through nested detail slots: programs-list → program-detail → program-session-detail.
	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}
	programDetail := byName["program-detail"]
	if programDetail.AttachedToFormulaID == nil || *programDetail.AttachedToFormulaID != byName["programs-list"].ID {
		t.Errorf("program-detail attached to %v, want programs-list", programDetail.AttachedToFormulaID)
	}
	if programDetail.Display == nil ||
		programDetail.Display.Slots.Detail != byName["program-session-detail"].ID.String() {
		t.Errorf("program-detail detail slot = %+v, want program-session-detail nested one level further",
			programDetail.Display)
	}

	// Program creation goes through the + Add dialog — an always-visible inline name box above the
	// cards would read as a search field. The search is the view's own query filter instead.
	for _, p := range byName["add-program"].Params {
		if p.Inline {
			t.Errorf("add-program param %s is inline, want the dialog-only add", p.Name)
		}
	}
	list = runFormula(t, deps, userID, bdbID, "programs-list", map[string]any{"query": "deload"})
	if len(list.Result.Rows) != 1 || cell(t, list.Result, 0, "name") != "Block B" {
		t.Fatalf("searched programs = %v, want the notes match alone", list.Result.Rows)
	}

	// The run-target fields gate on the kind choice, so a gym add shows a clean dialog.
	targetParams := []string{"target_distance_km", "target_pace", "target_duration_minutes"}
	for _, name := range []string{"add-program-session", "edit-program-session"} {
		for _, p := range byName[name].Params {
			gated := slices.Contains(targetParams, p.Name)
			if gated != (p.When != nil && p.When.Param == "kind" && p.When.Value == "run") {
				t.Errorf("%s param %s when = %+v, want target fields gated on kind = run and nothing else",
					name, p.Name, p.When)
			}
		}
	}
}

// TestWorkoutsLogSetCreatesExercise drives the creatable exercise field: logging with a name not in the
// catalog creates it on the fly, a different case resolves to the existing entry instead of forking it,
// and both exercise-taking actions declare the creatable picker.
func TestWorkoutsLogSetCreatesExercise(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})
	session := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM sessions", dbdomain.IntentQuery), 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "log-set",
		map[string]any{"parent_rowid": session, "exercise": "Hip thrust", "reps": 10.0})
	runFormula(t, deps, userID, bdbID, "log-set",
		map[string]any{"parent_rowid": session, "exercise": "hip THRUST", "reps": 8.0, "weight": 60.0})

	catalog := execSQL(t, deps, userID, bdbID,
		"SELECT id, name FROM exercises WHERE name = 'Hip thrust' COLLATE NOCASE", dbdomain.IntentQuery)
	if len(catalog.Rows) != 1 || cell(t, catalog, 0, "name") != "Hip thrust" {
		t.Fatalf("catalog = %v, want the one entry under its first spelling", catalog.Rows)
	}
	sets := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM exercise_sets WHERE exercise_id = (SELECT id FROM exercises"+
			" WHERE name = 'Hip thrust')", dbdomain.IntentQuery)
	if n := cell(t, sets, 0, "n"); n != int64(2) {
		t.Errorf("sets on the entry = %v, want both case spellings resolved to it", n)
	}

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	for _, f := range formulas {
		if f.Name != "log-set" && f.Name != "add-program-exercise" {
			continue
		}
		found := false
		for _, p := range f.Params {
			if p.Name == "exercise" {
				found = p.Lookup != nil && p.Lookup.AllowNew && p.Lookup.ValueColumn == "name"
			}
		}
		if !found {
			t.Errorf("%s exercise param: want an allow_new lookup over exercises by name", f.Name)
		}
	}
}

// TestWorkoutsSessionDetailOrder pins the set list as a plain running order: log-set takes the next free
// position in the session, so sets read back the way they were trained — two movements alternated stay
// alternated rather than being regrouped per exercise.
func TestWorkoutsSessionDetailOrder(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	session := gymSession(t, deps, userID, bdbID, "Pull-up", "Dip", "Pull-up", "Dip")

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT set_number FROM exercise_sets ORDER BY id", dbdomain.IntentQuery)
	for i, want := range []int64{1, 2, 3, 4} {
		if n := cell(t, stored, i, "set_number"); n != want {
			t.Errorf("set %d set_number = %v, want %d — positions run through the session", i, n, want)
		}
	}

	detail := runFormula(t, deps, userID, bdbID, "session-detail", map[string]any{"rowid": session})
	want := []string{"1: Pull-up — 8 reps", "2: Dip — 8 reps", "3: Pull-up — 8 reps", "4: Dip — 8 reps"}
	if got := lines(t, detail.Result); !slices.Equal(got, want) {
		t.Errorf("set lines = %v, want the order they were logged in %v", got, want)
	}
}

// TestWorkoutsEditSetNumber pins renumbering as the way to move a set: the detail view carries the stored
// position for the edit dialog to seed, reusing another set's position lands the set right after it, and a
// cleared position keeps the stored one rather than binding NULL into the NOT NULL column.
func TestWorkoutsEditSetNumber(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	session := gymSession(t, deps, userID, bdbID, "Pull-up", "Dip", "Pull-up", "Dip")
	// A set remembered after the fact appends at the end, wherever it actually belongs.
	runFormula(t, deps, userID, bdbID, "log-set",
		map[string]any{"parent_rowid": session, "exercise": "Pull-up", "reps": 6.0})

	detail := runFormula(t, deps, userID, bdbID, "session-detail", map[string]any{"rowid": session})
	if n := cell(t, detail.Result, 4, "set_number"); n != int64(5) {
		t.Fatalf("late set position = %v, want it appended at 5 and seeded to the edit dialog", n)
	}
	late := float64(cell(t, detail.Result, 4, "rowid").(int64))

	// Position 3 is the second pull-up set; sharing its number puts the late set immediately after it.
	runFormula(t, deps, userID, bdbID, "edit-set",
		map[string]any{"rowid": late, "set_number": 3.0, "reps": 6.0})
	detail = runFormula(t, deps, userID, bdbID, "session-detail", map[string]any{"rowid": session})
	want := []string{"1: Pull-up — 8 reps", "2: Dip — 8 reps", "3: Pull-up — 8 reps", "3: Pull-up — 6 reps",
		"4: Dip — 8 reps"}
	if got := lines(t, detail.Result); !slices.Equal(got, want) {
		t.Errorf("set lines after the move = %v, want the late set slotted into place %v", got, want)
	}

	runFormula(t, deps, userID, bdbID, "edit-set", map[string]any{"rowid": late, "reps": 7.0})
	stored := execSQL(t, deps, userID, bdbID, fmt.Sprintf(
		"SELECT set_number FROM exercise_sets WHERE id = %d", int64(late)), dbdomain.IntentQuery)
	if n := cell(t, stored, 0, "set_number"); n != int64(3) {
		t.Errorf("position after an edit that cleared it = %v, want the stored 3 kept", n)
	}
}

// TestWorkoutsProgramExercisePosition pins the prescription order as a manual position: add-program-exercise
// appends at the next one, the detail prefixes each line with it and seeds the stored value for the edit
// dialog, reusing another line's number lands the exercise right after it, a cleared position keeps the
// stored one, and the program card's plan line and a started session's skeleton follow the same order.
func TestWorkoutsProgramExercisePosition(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A",
	})
	program := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	for _, exercise := range []string{"Bench press", "Dip", "Overhead press"} {
		runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
			"parent_rowid": program, "exercise": exercise, "target_sets": 1.0, "target_reps": 5.0,
		})
	}

	detail := runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	want := []string{"1: Bench press — 1×5", "2: Dip — 1×5", "3: Overhead press — 1×5"}
	if got := lines(t, detail.Result); !slices.Equal(got, want) {
		t.Errorf("prescription lines = %v, want the order they were added in %v", got, want)
	}
	if n := cell(t, detail.Result, 2, "position"); n != int64(3) {
		t.Fatalf("late prescription position = %v, want it appended at 3 and seeded to the edit dialog", n)
	}
	overhead := float64(cell(t, detail.Result, 2, "rowid").(int64))

	// Sharing bench's position moves overhead press right after it, ahead of the dip.
	runFormula(t, deps, userID, bdbID, "edit-program-exercise", map[string]any{
		"rowid": overhead, "position": 1.0, "target_sets": 1.0, "target_reps": 5.0,
	})
	detail = runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	want = []string{"1: Bench press — 1×5", "1: Overhead press — 1×5", "2: Dip — 1×5"}
	if got := lines(t, detail.Result); !slices.Equal(got, want) {
		t.Errorf("prescription lines after the move = %v, want overhead press slotted after bench %v", got, want)
	}

	runFormula(t, deps, userID, bdbID, "edit-program-exercise", map[string]any{
		"rowid": overhead, "target_sets": 2.0, "target_reps": 5.0,
	})
	stored := execSQL(t, deps, userID, bdbID, fmt.Sprintf(
		"SELECT position FROM program_exercises WHERE id = %d", int64(overhead)), dbdomain.IntentQuery)
	if n := cell(t, stored, 0, "position"); n != int64(1) {
		t.Errorf("position after an edit that cleared it = %v, want the stored 1 kept", n)
	}

	card := runFormula(t, deps, userID, bdbID, "program-detail", map[string]any{"rowid": blockA})
	if v := cell(t, card.Result, 0, "display_body"); v != "Bench press 1×5 · Overhead press 2×5 · Dip 1×5" {
		t.Errorf("card plan line = %v, want it composed in prescription order", v)
	}

	runFormula(t, deps, userID, bdbID, "start-program-session", map[string]any{"rowid": program})
	skeleton := execSQL(t, deps, userID, bdbID,
		"SELECT e.name FROM exercise_sets es JOIN exercises e ON e.id = es.exercise_id"+
			" ORDER BY es.set_number", dbdomain.IntentQuery)
	wantSkeleton := []string{"Bench press", "Overhead press", "Overhead press", "Dip"}
	if len(skeleton.Rows) != len(wantSkeleton) {
		t.Fatalf("skeleton rows = %d, want %d", len(skeleton.Rows), len(wantSkeleton))
	}
	for i, name := range wantSkeleton {
		if v := cell(t, skeleton, i, "name"); v != name {
			t.Errorf("skeleton set %d = %v, want %s — the skeleton follows prescription order", i, v, name)
		}
	}
}

// TestWorkoutsReorderSlots pins the drag-to-reorder wiring on both ordered checklists: the set list and the
// prescription list carry the reorder slot, and running each slot's action with the rowids in a new order
// renumbers the position columns so the lists read back dropped-order first.
func TestWorkoutsReorderSlots(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}
	if d := byName["session-detail"].Display; d == nil || d.Slots.Reorder != byName["reorder-sets"].ID.String() {
		t.Errorf("session-detail reorder slot = %+v, want reorder-sets", d)
	}
	if d := byName["program-session-detail"].Display; d == nil ||
		d.Slots.Reorder != byName["reorder-program-exercises"].ID.String() {
		t.Errorf("program-session-detail reorder slot = %+v, want reorder-program-exercises", d)
	}

	session := gymSession(t, deps, userID, bdbID, "Pull-up", "Dip", "Push-up")
	detail := runFormula(t, deps, userID, bdbID, "session-detail", map[string]any{"rowid": session})
	ids := make([]any, len(detail.Result.Rows))
	for i := range detail.Result.Rows {
		// Reversed: the drop puts the last-logged set first.
		ids[len(ids)-1-i] = float64(cell(t, detail.Result, i, "rowid").(int64))
	}
	runFormula(t, deps, userID, bdbID, "reorder-sets", map[string]any{"ordered_rowids": ids})
	detail = runFormula(t, deps, userID, bdbID, "session-detail", map[string]any{"rowid": session})
	want := []string{"1: Push-up — 8 reps", "2: Dip — 8 reps", "3: Pull-up — 8 reps"}
	if got := lines(t, detail.Result); !slices.Equal(got, want) {
		t.Errorf("set lines after the drop = %v, want the reversed order %v", got, want)
	}

	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A",
	})
	program := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	for _, exercise := range []string{"Bench press", "Overhead press"} {
		runFormula(t, deps, userID, bdbID, "add-program-exercise", map[string]any{
			"parent_rowid": program, "exercise": exercise, "target_sets": 1.0, "target_reps": 5.0,
		})
	}
	prescriptions := runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	swapped := []any{
		float64(cell(t, prescriptions.Result, 1, "rowid").(int64)),
		float64(cell(t, prescriptions.Result, 0, "rowid").(int64)),
	}
	runFormula(t, deps, userID, bdbID, "reorder-program-exercises", map[string]any{"ordered_rowids": swapped})
	prescriptions = runFormula(t, deps, userID, bdbID, "program-session-detail", map[string]any{"rowid": program})
	wantLines := []string{"1: Overhead press — 1×5", "2: Bench press — 1×5"}
	if got := lines(t, prescriptions.Result); !slices.Equal(got, wantLines) {
		t.Errorf("prescription lines after the drop = %v, want the swapped order %v", got, wantLines)
	}
}

// gymSession starts a gym session and logs one 8-rep set of each exercise named, in order, returning the
// session's id.
func gymSession(t *testing.T, deps engineDeps, userID, bdbID uuid.UUID, exercises ...string) float64 {
	t.Helper()
	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})
	session := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM sessions ORDER BY id DESC LIMIT 1", dbdomain.IntentQuery), 0, "id").(int64))
	for _, exercise := range exercises {
		runFormula(t, deps, userID, bdbID, "log-set",
			map[string]any{"parent_rowid": session, "exercise": exercise, "reps": 8.0})
	}
	return session
}

// lines collects a result's line column top to bottom, the order the checklist renders.
func lines(t *testing.T, res *dbdomain.QueryResult) []string {
	t.Helper()
	out := make([]string, len(res.Rows))
	for i := range res.Rows {
		out[i], _ = cell(t, res, i, "line").(string)
	}
	return out
}

// TestWorkoutsDeleteProgramCascades pins the delete rules: dropping a program cascades its planned sessions
// and leaves logged workouts in place — their planned provenance just unlinks.
func TestWorkoutsDeleteProgramCascades(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-program", map[string]any{"name": "Block A"})
	blockA := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM programs", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "add-program-session", map[string]any{
		"parent_rowid": blockA, "name": "Push day A",
	})
	planned := float64(cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM program_sessions", dbdomain.IntentQuery), 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "start-program-session", map[string]any{"rowid": planned})

	runFormula(t, deps, userID, bdbID, "delete-program", map[string]any{"rowid": blockA})

	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM program_sessions", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("planned sessions = %v, want the cascade to clear the deleted program's", cell(t, n, 0, "n"))
	}
	logged := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n, MAX(program_session_id) AS provenance FROM sessions", dbdomain.IntentQuery)
	if cell(t, logged, 0, "n") != int64(1) || cell(t, logged, 0, "provenance") != nil {
		t.Errorf("logged sessions = %v, want the workout kept with its provenance unlinked", logged.Rows)
	}
}

// TestWorkoutsAddRunOneShot pins the single-step run entry: add-run creates the session and its runs row
// together (M:SS pace parsed to seconds), the Runs card composes the date—distance title and the per-aspect
// body lines, and the feed card composes the run stats where a gym card would show its sets.
func TestWorkoutsAddRunOneShot(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-run", map[string]any{
		"distance_km": 10.2, "pace": "5:30", "started_at": "2026-08-02T08:00", "avg_heart_rate": 152.0,
		"elevation_gain_m": 120.0, "route": "River loop", "location": "Lyon", "notes": "Felt good",
	})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT s.kind, s.started_at, r.distance_km, r.avg_pace_seconds_per_km "+
			"FROM sessions s JOIN runs r ON r.session_id = s.id", dbdomain.IntentQuery)
	if len(stored.Rows) != 1 {
		t.Fatalf("paired session+run rows = %d, want the two inserts joined 1:1", len(stored.Rows))
	}
	if v := cell(t, stored, 0, "kind"); v != "run" {
		t.Errorf("kind = %v, want run stamped by the formula", v)
	}
	if v := cell(t, stored, 0, "avg_pace_seconds_per_km"); v != int64(330) {
		t.Errorf("pace = %v, want 5:30 parsed to 330 seconds", v)
	}

	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	if len(list.Result.Rows) != 1 {
		t.Fatalf("run-list rows = %d, want the new run", len(list.Result.Rows))
	}
	if v := cell(t, list.Result, 0, "title"); v != "2026-08-02 — 10.2 km" {
		t.Errorf("run card title = %v, want the day and trimmed distance", v)
	}
	if v := cell(t, list.Result, 0,
		"display_body"); v != "5:30 /km · 56 min\n152 bpm · +120 m\nRiver loop · Lyon\nFelt good" {
		t.Errorf("run card body = %v, want one line per aspect with the duration derived from pace and distance", v)
	}
	if v := cell(t, list.Result, 0, "pace"); v != "5:30" {
		t.Errorf("run card pace seed = %v, want the stored seconds formatted back to M:SS", v)
	}
	if v := cell(t, list.Result, 0, "distance_km"); v != 10.2 {
		t.Errorf("raw distance_km = %v, want the stored value for the edit seed", v)
	}

	feed := runFormula(t, deps, userID, bdbID, "workouts", nil)
	if v := cell(t, feed.Result, 0,
		"summary"); v != "10.2 km · 5:30 /km · 152 bpm · +120 m\nRiver loop · Lyon\nFelt good" {
		t.Errorf("feed run summary = %v, want the composed run stats", v)
	}

	// The kinds are not exclusive containers: sets logged on a run session (strength during or after the
	// run) must surface on its feed card, under the run stats.
	session := float64(cell(t, feed.Result, 0, "rowid").(int64))
	for range 2 {
		runFormula(t, deps, userID, bdbID, "log-set",
			map[string]any{"parent_rowid": session, "exercise": "Push-up", "reps": 20.0})
	}
	feed = runFormula(t, deps, userID, bdbID, "workouts", nil)
	want := "10.2 km · 5:30 /km · 152 bpm · +120 m\nPush-up: 20 ×2\nRiver loop · Lyon\nFelt good"
	if v := cell(t, feed.Result, 0, "summary"); v != want {
		t.Errorf("hybrid run summary = %v, want the set breakdown under the run stats", v)
	}
}

// TestWorkoutsRunPaceForms pins the derive symmetry on add-run: plain-seconds pace passes through and fills
// the missing duration, an empty pace derives from duration and distance (the explicit duration stored as
// typed), and with neither given both stay NULL.
func TestWorkoutsRunPaceForms(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-run",
		map[string]any{"distance_km": 5.0, "pace": "330", "started_at": "2026-08-01T08:00"})
	runFormula(t, deps, userID, bdbID, "add-run",
		map[string]any{"distance_km": 10.0, "duration_minutes": 52.0, "started_at": "2026-08-02T08:00"})
	runFormula(t, deps, userID, bdbID, "add-run",
		map[string]any{"distance_km": 8.0, "started_at": "2026-08-03T08:00"})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT r.avg_pace_seconds_per_km AS pace, s.duration_minutes AS duration "+
			"FROM runs r JOIN sessions s ON s.id = r.session_id ORDER BY s.started_at", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "pace"); v != int64(330) {
		t.Errorf("plain-seconds pace = %v, want 330 passed through", v)
	}
	if v := cell(t, stored, 0, "duration"); v != int64(28) {
		t.Errorf("derived duration = %v, want 5 km at 330 s/km rounded to 28 min", v)
	}
	if v := cell(t, stored, 1, "pace"); v != int64(312) {
		t.Errorf("derived pace = %v, want 52 min over 10 km rounded to 312 s/km", v)
	}
	if v := cell(t, stored, 1, "duration"); v != int64(52) {
		t.Errorf("explicit duration = %v, want the typed value stored, not re-derived", v)
	}
	if v := cell(t, stored, 2, "pace"); v != nil {
		t.Errorf("underivable pace = %v, want NULL when neither pace nor duration is given", v)
	}
	if v := cell(t, stored, 2, "duration"); v != nil {
		t.Errorf("underivable duration = %v, want NULL when neither pace nor duration is given", v)
	}

	// printf never returns NULL, so a missing pace hangs on explicit guards: the newest card (the pace-less
	// run) must compose an empty body, not a phantom "0:00 /km".
	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	if v := cell(t, list.Result, 0, "title"); v != "2026-08-03 — 8 km" {
		t.Errorf("pace-less run title = %v, want the day and trimmed whole-number distance", v)
	}
	if v := cell(t, list.Result, 0, "display_body"); v != nil {
		t.Errorf("pace-less run body = %v, want empty with nothing to compose", v)
	}
}

// TestWorkoutsEditRunRoundTrip pins the Runs edit contract: the dialog seeds from the card's raw columns with
// pace pre-formatted as M:SS, and passing the seed back with corrections updates both the session and the run
// in one step — the M:SS seed parsing back to the same stored seconds.
func TestWorkoutsEditRunRoundTrip(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-run", map[string]any{
		"distance_km": 10.0, "pace": "5:30", "started_at": "2026-08-02T08:00", "notes": "Easy",
	})
	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	rowid := float64(cell(t, list.Result, 0, "rowid").(int64))

	runFormula(t, deps, userID, bdbID, "edit-run", map[string]any{
		"rowid": rowid, "distance_km": 10.5, "pace": cell(t, list.Result, 0, "pace"),
		"started_at": "2026-08-02 08:00:00", "notes": "Easy", "elevation_gain_m": 80.0,
	})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT s.started_at, s.notes, r.distance_km, r.avg_pace_seconds_per_km, r.elevation_gain_m "+
			"FROM sessions s JOIN runs r ON r.session_id = s.id", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "distance_km"); v != 10.5 {
		t.Errorf("distance after edit = %v, want the correction", v)
	}
	if v := cell(t, stored, 0, "avg_pace_seconds_per_km"); v != int64(330) {
		t.Errorf("pace after edit = %v, want the M:SS seed parsed back to the same 330 seconds", v)
	}
	if v := cell(t, stored, 0, "elevation_gain_m"); v != int64(80) {
		t.Errorf("elevation after edit = %v, want the addition", v)
	}
	if v := cell(t, stored, 0, "started_at"); v != "2026-08-02 08:00:00" {
		t.Errorf("started_at after edit = %v, want the seeded value accepted as-is", v)
	}
}

// TestWorkoutsRunSessionAlwaysListed pins the pairing contract: a run added through add-session gets its
// empty runs row immediately and lists in the Runs tab as a date-only stub (a gym session gets none), and
// log-run fills the stub's stats — its empty pace derived from the session's duration.
func TestWorkoutsRunSessionAlwaysListed(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-session",
		map[string]any{"kind": "run", "duration_minutes": 30.0, "started_at": "2026-08-05T08:00"})

	stub := execSQL(t, deps, userID, bdbID, "SELECT distance_km FROM runs", dbdomain.IntentQuery)
	if len(stub.Rows) != 1 {
		t.Fatalf("runs rows = %d, want the empty run row seeded by add-session", len(stub.Rows))
	}
	if v := cell(t, stub, 0, "distance_km"); v != nil {
		t.Errorf("stub distance = %v, want NULL until filled in", v)
	}

	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	if len(list.Result.Rows) != 1 {
		t.Fatalf("run-list rows = %d, want the stub listed", len(list.Result.Rows))
	}
	if v := cell(t, list.Result, 0, "title"); v != "2026-08-05" {
		t.Errorf("stub title = %v, want the date alone with no distance yet", v)
	}
	if v := cell(t, list.Result, 0, "display_body"); v != "30 min" {
		t.Errorf("stub body = %v, want only the session's duration with no run stats yet", v)
	}

	runFormula(t, deps, userID, bdbID, "add-session", map[string]any{"kind": "gym"})
	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM runs", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(1) {
		t.Errorf("runs rows after a gym add = %v, want no run row for a gym session", cell(t, n, 0, "n"))
	}

	rowid := float64(cell(t, list.Result, 0, "rowid").(int64))
	runFormula(t, deps, userID, bdbID, "log-run", map[string]any{"rowid": rowid, "distance_km": 5.0})
	stored := execSQL(t, deps, userID, bdbID, "SELECT avg_pace_seconds_per_km FROM runs", dbdomain.IntentQuery)
	if v := cell(t, stored, 0, "avg_pace_seconds_per_km"); v != int64(360) {
		t.Errorf("log-run derived pace = %v, want 30 min over 5 km as 360 s/km", v)
	}
}

// TestWorkoutsEditRunPairsBareSession pins the safety net for run sessions created without their runs row
// (raw SQL rather than the actions): the Runs tab still lists them, and edit-run's upsert pairs one with
// the values typed instead of silently updating zero rows.
func TestWorkoutsEditRunPairsBareSession(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	execSQL(t, deps, userID, bdbID,
		"INSERT INTO sessions (kind, started_at) VALUES ('run', '2026-08-06 09:00:00')", dbdomain.IntentExecute)

	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	if len(list.Result.Rows) != 1 {
		t.Fatalf("run-list rows = %d, want the bare session listed", len(list.Result.Rows))
	}
	rowid := float64(cell(t, list.Result, 0, "rowid").(int64))

	runFormula(t, deps, userID, bdbID, "edit-run", map[string]any{
		"rowid": rowid, "distance_km": 8.0, "pace": "5:00", "started_at": "2026-08-06 09:00:00",
	})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT distance_km, avg_pace_seconds_per_km FROM runs", dbdomain.IntentQuery)
	if len(stored.Rows) != 1 {
		t.Fatalf("runs rows = %d, want the edit to have paired the session", len(stored.Rows))
	}
	if v := cell(t, stored, 0, "distance_km"); v != 8.0 {
		t.Errorf("paired distance = %v, want the typed value", v)
	}
	if v := cell(t, stored, 0, "avg_pace_seconds_per_km"); v != int64(300) {
		t.Errorf("paired pace = %v, want 5:00 parsed to 300 seconds", v)
	}
}

// TestWorkoutsDeleteRunCascades pins the Runs delete slot: deleting from the card removes the session and its
// run details in one go.
func TestWorkoutsDeleteRunCascades(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	runFormula(t, deps, userID, bdbID, "add-run", map[string]any{"distance_km": 5.0})
	list := runFormula(t, deps, userID, bdbID, "run-list", nil)
	rowid := float64(cell(t, list.Result, 0, "rowid").(int64))

	runFormula(t, deps, userID, bdbID, "delete-run", map[string]any{"rowid": rowid})

	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM sessions", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("sessions left = %v, want the run's session gone", cell(t, n, 0, "n"))
	}
	if n := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM runs", dbdomain.IntentQuery); cell(t, n, 0, "n") != int64(0) {
		t.Errorf("runs left = %v, want the details cascaded away", cell(t, n, 0, "n"))
	}
}

// TestWorkoutsRunSummary pins the hidden rollup: count, total and longest distance, best and average pace in
// M:SS /km form, and the :days window keeping only recent runs.
func TestWorkoutsRunSummary(t *testing.T) {
	deps, userID, bdbID := applyWorkouts(t)

	// One run stamped now (inside any :days window) and one long past (outside every reasonable window).
	runFormula(t, deps, userID, bdbID, "add-run", map[string]any{"distance_km": 5.0, "pace": "5:00"})
	runFormula(t, deps, userID, bdbID, "add-run",
		map[string]any{"distance_km": 12.0, "pace": "6:00", "started_at": "2020-01-01T08:00"})

	all := runFormula(t, deps, userID, bdbID, "run-summary", nil)
	if v := cell(t, all.Result, 0, "runs"); v != int64(2) {
		t.Errorf("runs = %v, want both counted with no window", v)
	}
	if v := cell(t, all.Result, 0, "total_km"); v != 17.0 {
		t.Errorf("total_km = %v, want the distances summed", v)
	}
	if v := cell(t, all.Result, 0, "longest_run_km"); v != 12.0 {
		t.Errorf("longest_run_km = %v, want the longest distance", v)
	}
	if v := cell(t, all.Result, 0, "best_pace"); v != "5:00 /km" {
		t.Errorf("best_pace = %v, want the fastest pace formatted", v)
	}
	if v := cell(t, all.Result, 0, "avg_pace"); v != "5:30 /km" {
		t.Errorf("avg_pace = %v, want the unweighted average formatted", v)
	}

	recent := runFormula(t, deps, userID, bdbID, "run-summary", map[string]any{"days": 7.0})
	if v := cell(t, recent.Result, 0, "runs"); v != int64(1) {
		t.Errorf("windowed runs = %v, want only the recent one", v)
	}
	if v := cell(t, recent.Result, 0, "total_km"); v != 5.0 {
		t.Errorf("windowed total_km = %v, want only the recent distance", v)
	}
}
