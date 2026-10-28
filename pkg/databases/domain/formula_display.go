package domain

import (
	"fmt"
	"iter"
	"slices"
)

// DisplayMode selects how the app renders a view formula's rows. A nil display config also renders
// a table — the bare default with no declared slots.
type DisplayMode string

const (
	// DisplayModeTable renders rows as the plain column grid; a stored table config exists to
	// declare slots on it (currently only detail).
	DisplayModeTable DisplayMode = "table"

	// DisplayModeChecklist renders rows as a dense single-line checklist: a title per row, an
	// optional checkbox, and an inline add input when the add slot is filled.
	DisplayModeChecklist DisplayMode = "checklist"

	// DisplayModeCards renders rows as a grid of note cards: a title and an optional body preview
	// per card.
	DisplayModeCards DisplayMode = "cards"

	// DisplayModeBoard renders rows as cards laid out in side-by-side columns, one column per value
	// of the group-role column. With a move slot filled, dragging a card into another column runs
	// the slotted action against the dropped row and the destination column's value.
	DisplayModeBoard DisplayMode = "board"

	// DisplayModeCalendar renders rows as cards placed on the day named by the day-role column,
	// across a range the app chooses and binds to the view's reserved :window_start / :window_end
	// params; rows with a NULL date sit in an unscheduled tray beside the days. Within a day, rows
	// keep the order the view returned them in — the view's ORDER BY decides, by time when the day
	// column carries one, by a position column when a reorder slot renumbers it. With a move slot
	// filled, dragging a card onto another day runs the slotted action against the dropped row and
	// that day.
	DisplayModeCalendar DisplayMode = "calendar"
)

// DisplaySlot names an interaction surface of a display rendering that one of the view's attached
// formulas can fill — an action for the mutation slots, a view for the detail slot. Which slots a
// mode accepts is checked by ValidateFormulaDisplay.
type DisplaySlot string

const (
	// DisplaySlotAdd inserts a new row (checklist: inline input; cards: add button).
	DisplaySlotAdd DisplaySlot = "add"

	// DisplaySlotToggle flips a row's done state from its checkbox.
	DisplaySlotToggle DisplaySlot = "toggle"

	// DisplaySlotEdit updates a row from a form seeded with its current values.
	DisplaySlotEdit DisplaySlot = "edit"

	// DisplaySlotDelete removes a row.
	DisplaySlotDelete DisplaySlot = "delete"

	// DisplaySlotDetail opens one row's full record, rendered from the named attached row-detail
	// view. The only slot filled by a view rather than an action. A detail view may carry a detail
	// slot of its own, and so may that one — the drill nests to any depth the attachment chain
	// reaches; the chain is acyclic by construction, since a formula only attaches to a pre-existing view.
	DisplaySlotDetail DisplaySlot = "detail"

	// DisplaySlotBulk is the ordered list of bulk actions filling the selection toolbar; each entry
	// binds the checked rows as :rowids.
	DisplaySlotBulk DisplaySlot = "bulk"

	// DisplaySlotRowActions is the ordered per-row menu: extra single-row actions — the view's own
	// verbs, each binding the tapped row as :rowid — and, optionally, the edit, delete, or detail
	// slot's own formula, which places that built-in menu entry at its position. Built-in entries
	// left unlisted trail the list in the order details, edit, delete.
	DisplaySlotRowActions DisplaySlot = "row_actions"

	// DisplaySlotReorder enables drag-to-reorder on a checklist, a cards grid, or within a board
	// column: dropping a row runs the action with the listed rowids in their new display order as
	// :ordered_rowids — every row on a checklist or cards grid, the affected column's rows on a
	// board — one run for the whole drag. The action's SQL owns the persistence — typically
	// renumbering an order column from the array index, which stays correct on a board's subset
	// because ordering there is per-column.
	DisplaySlotReorder DisplaySlot = "reorder"

	// DisplaySlotMove enables drag-across-columns on a board, or drag-across-days on a calendar:
	// dropping a card into another column (day) runs the action with the dragged row as :rowid and
	// the destination's value bound to the action's target parameter — the single required
	// parameter, which on a board carries the options that also define the columns and their order,
	// and on a calendar is a date the drop binds the day to. The action may additionally declare
	// an optional :ordered_rowids, bound to the destination column's rowids in their post-drop
	// order so one atomic run both re-homes and places the card; it is omitted when the visible
	// rows are a subset (a filter or pagination), so the renumbering statement must no-op on NULL.
	DisplaySlotMove DisplaySlot = "move"
)

// FormulaDisplaySlots maps interaction slots to formulas **attached to the view** — never to
// arbitrary formulas of the bdb. That restriction is a safety boundary, not an organizational
// choice: row-bound slots fire with the tapped row's rowid, and SQLite rowids are table-local, so
// a formula shared across views could silently hit the wrong table's row. Attachment ties each
// slotted formula to the one view whose rows it was written for, and cascades its lifecycle.
// Stored refs are canonical formula UUIDs — authoring surfaces may accept a formula name and
// resolve it before storage, so a later rename never breaks a slot. An empty ref leaves its slot
// unfilled.
type FormulaDisplaySlots struct {
	Add     string
	Toggle  string
	Edit    string
	Delete  string
	Detail  string
	Reorder string
	Move    string

	// Bulk and RowActions are ordered lists — array order is the render order (the toolbar and the
	// per-row menu), unlike the fixed slots above which hold at most one ref each. An attached
	// formula holds at most one role across all slots and lists; the one exception is a RowActions
	// entry repeating the edit, delete, or detail ref, which positions that entry rather than
	// claiming a second role.
	Bulk       []string
	RowActions []string
}

// PlacesInRowMenu reports whether the slot renders as a built-in entry of the per-row menu, the entries a
// row_actions ref may position by naming the slot's formula.
func (s DisplaySlot) PlacesInRowMenu() bool {
	return s == DisplaySlotEdit || s == DisplaySlotDelete || s == DisplaySlotDetail
}

// All yields the filled (slot, ref) pairs in declaration order (add, toggle, edit, delete,
// detail, reorder, move, then each bulk entry, then each row_actions entry), skipping unfilled slots. List
// entries repeat their slot name, one yield per entry, so per-ref consumers (resolution, the
// symmetric guards) cover lists on the same code path as the fixed slots.
func (a FormulaDisplaySlots) All() iter.Seq2[DisplaySlot, string] {
	return func(yield func(DisplaySlot, string) bool) {
		pairs := []struct {
			slot DisplaySlot
			ref  string
		}{
			{DisplaySlotAdd, a.Add},
			{DisplaySlotToggle, a.Toggle},
			{DisplaySlotEdit, a.Edit},
			{DisplaySlotDelete, a.Delete},
			{DisplaySlotDetail, a.Detail},
			{DisplaySlotReorder, a.Reorder},
			{DisplaySlotMove, a.Move},
		}

		for _, p := range pairs {
			if p.ref != "" && !yield(p.slot, p.ref) {
				return
			}
		}
		for _, ref := range a.Bulk {
			if ref != "" && !yield(DisplaySlotBulk, ref) {
				return
			}
		}
		for _, ref := range a.RowActions {
			if ref != "" && !yield(DisplaySlotRowActions, ref) {
				return
			}
		}
	}
}

// Set stores ref in the given slot; a list slot appends, preserving call order. Unknown slots are
// ignored.
func (a *FormulaDisplaySlots) Set(slot DisplaySlot, ref string) {
	switch slot {
	case DisplaySlotAdd:
		a.Add = ref
	case DisplaySlotToggle:
		a.Toggle = ref
	case DisplaySlotEdit:
		a.Edit = ref
	case DisplaySlotDelete:
		a.Delete = ref
	case DisplaySlotDetail:
		a.Detail = ref
	case DisplaySlotReorder:
		a.Reorder = ref
	case DisplaySlotMove:
		a.Move = ref
	case DisplaySlotBulk:
		a.Bulk = append(a.Bulk, ref)
	case DisplaySlotRowActions:
		a.RowActions = append(a.RowActions, ref)
	}
}

// IsEmpty reports whether no slot or list entry is filled.
func (a FormulaDisplaySlots) IsEmpty() bool {
	for range a.All() {
		return false
	}
	return true
}

// FormulaDisplay is a view formula's optional presentation config: how the app renders the rows
// (table, checklist, or cards) and which attached formulas fill the rendering's interaction slots. It changes
// presentation only — a slotted action runs through the same execution path and permission checks
// as running it directly.
type FormulaDisplay struct {
	Mode DisplayMode

	// Columns declares per-column presentation: which result column plays which role in the mode's
	// rendering, and how its value is formatted. One entry per column; each entry carries a role,
	// a format, or both.
	Columns []FormulaDisplayColumn

	Slots FormulaDisplaySlots

	// Window says which days a calendar shows when it opens and how far its arrows move. Calendar mode
	// only; nil is the Monday-to-Sunday week.
	Window *FormulaDisplayWindow
}

// FormulaDisplayWindow is the span of days a calendar shows, one of two kinds. A weekly window names its
// first and last weekday, repeats every 7 days, and is the kind an absent window means (Monday to Sunday);
// an absent WeekStart is Monday, an absent WeekEnd closes a full week from WeekStart, and the pair may wrap
// ("saturday" to "sunday" is a weekend, "sunday" to "monday" two days). A fixed range names its first day and
// a length in days, steps by that length, and is 7 days long when Days is absent. The two kinds never mix: a
// window carries either weekday names or a start and a length.
type FormulaDisplayWindow struct {
	WeekStart DisplayWeekday
	WeekEnd   DisplayWeekday

	// Start is the fixed range's first day as "YYYY-MM-DD".
	Start string

	// Days is the fixed range's length, 1 to MaxCalendarWindowDays; 0 stands for absent.
	Days int
}

// IsFixed reports whether the window is a fixed range rather than a weekly one.
func (w FormulaDisplayWindow) IsFixed() bool {
	return w.Start != "" || w.Days != 0
}

// MaxCalendarWindowDays is the longest fixed range a calendar shows: a month fits, and anything longer
// wants a different layout rather than a longer strip of days.
const MaxCalendarWindowDays = 31

// DisplayWeekday names a day of the week in a calendar window by its lowercase English name.
type DisplayWeekday string

// The seven weekday names a weekly window is declared with.
const (
	DisplayWeekdayMonday    DisplayWeekday = "monday"
	DisplayWeekdayTuesday   DisplayWeekday = "tuesday"
	DisplayWeekdayWednesday DisplayWeekday = "wednesday"
	DisplayWeekdayThursday  DisplayWeekday = "thursday"
	DisplayWeekdayFriday    DisplayWeekday = "friday"
	DisplayWeekdaySaturday  DisplayWeekday = "saturday"
	DisplayWeekdaySunday    DisplayWeekday = "sunday"
)

// DisplayWeekdays lists the weekday names in week order, Monday first.
var DisplayWeekdays = []DisplayWeekday{
	DisplayWeekdayMonday, DisplayWeekdayTuesday, DisplayWeekdayWednesday, DisplayWeekdayThursday,
	DisplayWeekdayFriday, DisplayWeekdaySaturday, DisplayWeekdaySunday,
}

// Display column roles: which part of the mode's rendering a result column feeds.
const (
	// DisplayRoleTitle is each row's primary text. Required for checklist and cards.
	DisplayRoleTitle DisplayColumnRole = "title"

	// DisplayRoleBody is the preview under a card's title. Cards and board modes only.
	DisplayRoleBody DisplayColumnRole = "body"

	// DisplayRoleCheck is the 0/1 column rendered as each row's checkbox. Checklist mode only.
	DisplayRoleCheck DisplayColumnRole = "check"

	// DisplayRoleGroup is the column whose value places each row in a board column. Board mode
	// only, and required there.
	DisplayRoleGroup DisplayColumnRole = "group"

	// DisplayRoleDay is the column whose value places each row on a calendar day: a "YYYY-MM-DD" on
	// that day, a "YYYY-MM-DD HH:MM:SS" on its day too and within it where a layout reads the time;
	// a NULL lands the row in the unscheduled tray. Calendar mode only, and required there.
	DisplayRoleDay DisplayColumnRole = "day"
)

// DisplayColumnRole names the rendering part a column feeds (title / body / check / group).
type DisplayColumnRole string

// DisplayFormatMarkdown renders the column's text as simple markdown (headings, emphasis, lists,
// links, inline code). Presentation-only — the stored value stays raw markdown source.
const DisplayFormatMarkdown DisplayColumnFormat = "markdown"

// DisplayColumnFormat names how a column's value is rendered ("" = plain text).
type DisplayColumnFormat string

// FormulaDisplayColumn assigns one result column a role in the rendering and/or a value format, or
// marks it hidden. A hidden column is dropped from the table grid but stays in the row data, so it
// can carry machine-shaped values (e.g. a JSON array seeding a group param's edit dialog) without
// cluttering the display. Hidden is exclusive with role/format — a column that renders can't be
// hidden.
type FormulaDisplayColumn struct {
	Column string
	Role   DisplayColumnRole
	Format DisplayColumnFormat
	Hidden bool
}

// RoleColumn returns the name of the column holding the given role ("" when unassigned). Safe on a
// nil display.
func (d *FormulaDisplay) RoleColumn(role DisplayColumnRole) string {
	if d == nil {
		return ""
	}
	for _, c := range d.Columns {
		if c.Role == role {
			return c.Column
		}
	}
	return ""
}

// ValidateFormulaDisplay checks a display config against the view's result columns and parameter schema:
//
//   - mode: one of table / checklist / cards / board / calendar.
//   - column roles, mode-gated: title for checklist/cards/board/calendar; body for cards/board/calendar;
//     check for checklist; group for board and day for calendar — each required in its mode. A table renders
//     all columns and assigns no roles.
//   - named columns: every column entry must name a column the view's result actually returns.
//   - slots, mode-gated: the bulk list is table-only — the grid is the sole multi-selection surface;
//     reorder is checklist, cards, board, or calendar — a row list, a grid, a column, or a day is the drag
//     surface; move is board or calendar — the columns or the days are the drop surface; toggle is rejected
//     by table, board, and calendar — it renders through the check role, which only a checklist carries. A
//     table takes every other slot.
//   - rowid: a row-bound slot or list (toggle / edit / delete / detail / bulk / row_actions / reorder /
//     move) requires the view to select `rowid AS rowid`.
//   - pagination: the reorder slot requires an unpaginated view — a drag can only order the rows it can
//     see, and renumbering one page against unseen neighbours would scramble them.
//   - calendar: the view must declare the window_start / window_end pair and cannot paginate — the window
//     is the whole contract of what the calendar shows.
//   - window: calendar-only; a weekly window names known weekdays, a fixed range a real day and a length
//     within MaxCalendarWindowDays, and never both kinds at once.
//
// attached marks a config on a row-detail view (rendered inside its parent's modal): its interactive
// slots need checklist/cards/board/calendar mode — including a detail slot of its own, nesting the drill
// recursively to any depth — and it cannot carry a bulk list (the modal has no multi-selection).
// Slot refs are resolved against the view's attached formulas by the caller, which owns those checks.
func ValidateFormulaDisplay(d FormulaDisplay, resultColumns []string, viewParams ParamSchema, attached bool) error {
	switch d.Mode {
	case DisplayModeTable, DisplayModeChecklist, DisplayModeCards, DisplayModeBoard, DisplayModeCalendar:
	default:
		return fmt.Errorf("%w: unknown mode %q", ErrFormulaDisplayInvalid, d.Mode)
	}
	if attached {
		// Table mode on a detail view carries column formats only (the record card / static table
		// keeps rendering); interactive table slots exist solely on top-level views.
		if d.Mode == DisplayModeTable && !d.Slots.IsEmpty() {
			return fmt.Errorf("%w: a detail view's table display carries column formats only, no slots",
				ErrFormulaDisplayInvalid)
		}
	}
	if len(d.Slots.Bulk) > 0 && d.Mode != DisplayModeTable {
		return fmt.Errorf("%w: the bulk list requires %s mode — the selection toolbar exists only there",
			ErrFormulaDisplayInvalid, DisplayModeTable)
	}
	if d.Slots.Move != "" && d.Mode != DisplayModeBoard && d.Mode != DisplayModeCalendar {
		return fmt.Errorf("%w: the move slot requires %s or %s mode — their columns or days are the drop surface",
			ErrFormulaDisplayInvalid, DisplayModeBoard, DisplayModeCalendar)
	}
	if d.Slots.Reorder != "" {
		switch d.Mode {
		case DisplayModeChecklist, DisplayModeCards, DisplayModeBoard, DisplayModeCalendar:
		default:
			return fmt.Errorf("%w: the reorder slot requires %s, %s, %s, or %s mode — their row lists are the"+
				" drag surfaces", ErrFormulaDisplayInvalid, DisplayModeChecklist, DisplayModeCards, DisplayModeBoard,
				DisplayModeCalendar)
		}
		for _, p := range viewParams {
			if p.Source == ParamSourcePagination {
				return fmt.Errorf("%w: the reorder slot is not valid on a paginated view — a drag can only"+
					" order rows it can see, and one page renumbered against unseen neighbours would scramble them",
					ErrFormulaDisplayInvalid)
			}
		}
	}
	if err := validateDisplayColumns(d, resultColumns); err != nil {
		return err
	}
	if d.Mode == DisplayModeTable {
		// The one slot a table cannot take: toggle renders through the check role, a checklist
		// concept, and table mode assigns no roles.
		if d.Slots.Toggle != "" {
			return fmt.Errorf("%w: a table display does not support the toggle slot",
				ErrFormulaDisplayInvalid)
		}
	} else if d.RoleColumn(DisplayRoleTitle) == "" {
		return fmt.Errorf("%w: a %s display requires a title-role column", ErrFormulaDisplayInvalid, d.Mode)
	}
	if d.Mode == DisplayModeBoard {
		// Same rationale as the table rejection: toggle renders through the check role, and a board
		// card carries no checkbox.
		if d.Slots.Toggle != "" {
			return fmt.Errorf("%w: a board display does not support the toggle slot",
				ErrFormulaDisplayInvalid)
		}
		if d.RoleColumn(DisplayRoleGroup) == "" {
			return fmt.Errorf("%w: a board display requires a group-role column — its value places each row"+
				" in a column", ErrFormulaDisplayInvalid)
		}
	}
	if d.Mode == DisplayModeCalendar {
		if d.Slots.Toggle != "" {
			return fmt.Errorf("%w: a calendar display does not support the toggle slot",
				ErrFormulaDisplayInvalid)
		}
		if d.RoleColumn(DisplayRoleDay) == "" {
			return fmt.Errorf("%w: a calendar display requires a day-role column — its value places each row"+
				" on a day", ErrFormulaDisplayInvalid)
		}
		// The visible range is the calendar's whole contract: without the window pair the app could
		// only ever show a week out of an unbounded result, and pagination would page across days the
		// range already narrows to.
		hasWindow := false
		for _, p := range viewParams {
			if p.Source == ParamSourceWindow {
				hasWindow = true
			}
			if p.Source == ParamSourcePagination {
				return fmt.Errorf("%w: a calendar display is not valid on a paginated view — the %q / %q window"+
					" bounds its rows", ErrFormulaDisplayInvalid, ReservedParamWindowStart, ReservedParamWindowEnd)
			}
		}
		if !hasWindow {
			return fmt.Errorf("%w: a calendar display requires the view to declare the %q / %q window"+
				" parameters (source %q) that the app binds to the visible range",
				ErrFormulaDisplayInvalid, ReservedParamWindowStart, ReservedParamWindowEnd, ParamSourceWindow)
		}
		if d.Window != nil {
			if err := validateDisplayWindow(*d.Window); err != nil {
				return err
			}
		}
	} else if d.Window != nil {
		return fmt.Errorf("%w: a window is only valid on a calendar display — it says which days the calendar"+
			" shows", ErrFormulaDisplayInvalid)
	}

	// The bulk list counts as row-bound too: its checkboxes read each row's rowid for :rowids, as
	// does reorder: the drag reads each row's rowid to report the new order, and move: the drop
	// reports the dragged row's rowid.
	rowBound := d.Slots.Toggle != "" || d.Slots.Edit != "" || d.Slots.Delete != "" || d.Slots.Detail != "" ||
		d.Slots.Reorder != "" || d.Slots.Move != "" || len(d.Slots.Bulk) > 0 || len(d.Slots.RowActions) > 0
	if rowBound && !slices.Contains(resultColumns, ReservedParamRowID) {
		return fmt.Errorf("%w: a row-bound slot requires the view to select `rowid AS rowid`",
			ErrFormulaDisplayInvalid)
	}
	return nil
}

// validateDisplayWindow checks a calendar window is one kind or the other: weekday names come from the
// seven known ones, a fixed range passes validateFixedWindow, and a window naming weekdays next to a start
// or a length is refused rather than read one way or the other.
func validateDisplayWindow(w FormulaDisplayWindow) error {
	weekly := w.WeekStart != "" || w.WeekEnd != ""
	if weekly && w.IsFixed() {
		return fmt.Errorf("%w: a window is either weekly (week_start / week_end) or a fixed range (start / days),"+
			" not both", ErrFormulaDisplayInvalid)
	}
	for _, day := range []DisplayWeekday{w.WeekStart, w.WeekEnd} {
		if day != "" && !slices.Contains(DisplayWeekdays, day) {
			return fmt.Errorf("%w: unknown weekday %q in the window — use one of %v",
				ErrFormulaDisplayInvalid, day, DisplayWeekdays)
		}
	}
	if w.IsFixed() {
		return validateFixedWindow(w)
	}
	return nil
}

// validateFixedWindow checks a fixed-range window: Start is a real calendar day written as YYYY-MM-DD, and
// Days is either absent (0) or within 1 and MaxCalendarWindowDays; a length without a start day is refused,
// since the range would have nowhere to begin. Errors wrap ErrFormulaDisplayInvalid and name the field.
func validateFixedWindow(w FormulaDisplayWindow) error {
	if w.Start == "" {
		return fmt.Errorf("%w: a window length (days) needs a start day", ErrFormulaDisplayInvalid)
	}
	if _, ok := CanonicalDate(w.Start); !ok {
		return fmt.Errorf("%w: window start %q must be a calendar day written as YYYY-MM-DD",
			ErrFormulaDisplayInvalid, w.Start)
	}
	if w.Days != 0 && (w.Days < 1 || w.Days > MaxCalendarWindowDays) {
		return fmt.Errorf("%w: window days %d must be between 1 and %d",
			ErrFormulaDisplayInvalid, w.Days, MaxCalendarWindowDays)
	}
	return nil
}

// validateDisplayColumns checks the per-column entries:
//
//   - naming: each entry names a distinct column the view's result actually returns.
//   - content: each entry carries a known role and/or format, or is hidden.
//   - roles, singular and mode-gated: title for checklist/cards/board/calendar; body for
//     cards/board/calendar; check checklist-only; group board-only; day calendar-only. A table
//     renders all non-hidden columns and assigns no roles.
//   - format: valid in any mode.
//   - hidden: exclusive with role and format — the column is dropped from the grid but kept in the
//     row data.
func validateDisplayColumns(d FormulaDisplay, resultColumns []string) error {
	seenCols := make(map[string]struct{}, len(d.Columns))
	seenRoles := make(map[DisplayColumnRole]struct{}, len(d.Columns))
	for _, c := range d.Columns {
		if c.Column == "" {
			return fmt.Errorf("%w: a display column entry must name a column", ErrFormulaDisplayInvalid)
		}
		if !slices.Contains(resultColumns, c.Column) {
			return fmt.Errorf("%w: column %q is not returned by the view", ErrFormulaDisplayInvalid, c.Column)
		}
		if _, dup := seenCols[c.Column]; dup {
			return fmt.Errorf("%w: column %q is declared twice", ErrFormulaDisplayInvalid, c.Column)
		}
		seenCols[c.Column] = struct{}{}

		if c.Hidden {
			if c.Role != "" || c.Format != "" {
				return fmt.Errorf("%w: hidden column %q must not also carry a role or format — it does not render",
					ErrFormulaDisplayInvalid, c.Column)
			}
			continue
		}
		if c.Role == "" && c.Format == "" {
			return fmt.Errorf("%w: column %q declares neither a role, a format, nor hidden",
				ErrFormulaDisplayInvalid, c.Column)
		}
		switch c.Role {
		case "":
		case DisplayRoleTitle, DisplayRoleBody, DisplayRoleCheck, DisplayRoleGroup, DisplayRoleDay:
			if d.Mode == DisplayModeTable {
				return fmt.Errorf("%w: a table display renders all columns and assigns no roles",
					ErrFormulaDisplayInvalid)
			}
			if _, dup := seenRoles[c.Role]; dup {
				return fmt.Errorf("%w: role %q is assigned twice", ErrFormulaDisplayInvalid, c.Role)
			}
			seenRoles[c.Role] = struct{}{}
			if c.Role == DisplayRoleBody && d.Mode != DisplayModeCards && d.Mode != DisplayModeBoard &&
				d.Mode != DisplayModeCalendar {
				return fmt.Errorf("%w: the body role is only valid in %s, %s, or %s mode",
					ErrFormulaDisplayInvalid, DisplayModeCards, DisplayModeBoard, DisplayModeCalendar)
			}
			if c.Role == DisplayRoleCheck && d.Mode != DisplayModeChecklist {
				return fmt.Errorf("%w: the check role is only valid in %s mode",
					ErrFormulaDisplayInvalid, DisplayModeChecklist)
			}
			if c.Role == DisplayRoleGroup && d.Mode != DisplayModeBoard {
				return fmt.Errorf("%w: the group role is only valid in %s mode",
					ErrFormulaDisplayInvalid, DisplayModeBoard)
			}
			if c.Role == DisplayRoleDay && d.Mode != DisplayModeCalendar {
				return fmt.Errorf("%w: the day role is only valid in %s mode",
					ErrFormulaDisplayInvalid, DisplayModeCalendar)
			}
		default:
			return fmt.Errorf("%w: unknown column role %q", ErrFormulaDisplayInvalid, c.Role)
		}
		if c.Format != "" && c.Format != DisplayFormatMarkdown {
			return fmt.Errorf("%w: unknown column format %q", ErrFormulaDisplayInvalid, c.Format)
		}
	}
	return nil
}

// SlotReferencing returns the action slot holding ref, if any. Safe on a nil display.
func (d *FormulaDisplay) SlotReferencing(ref string) (DisplaySlot, bool) {
	if d == nil {
		return "", false
	}
	for slot, r := range d.Slots.All() {
		if r == ref {
			return slot, true
		}
	}
	return "", false
}

// displaySlotRule pins the action shape a display slot requires:
//
//   - verb: the required statement verb; empty accepts any derived verb — list entries carry their
//     own button/menu label, an edit may rewrite owned children, and a toggle may record its flip
//     in another table, so no single verb applies.
//   - needsRowID / needsRowIDs / needsOrderedRowIDs: the selection binding the interaction
//     supplies — the tapped row, the checked rows, or the dragged row order.
//   - allowsOrderedRowIDs: admits an optional :ordered_rowids without requiring it — the move
//     slot's placement binding, supplied only when the app sees every row of the target column.
//   - othersOptional: every remaining parameter must be optional — the interaction supplies
//     nothing beyond the row.
//   - needsPromptParam: at least one prompt/lookup parameter is required — an edit without a form
//     field would open an empty dialog.
//   - needsTargetParam: exactly one required option-declaring parameter — the target a board drop
//     binds the destination column's value to — with every other parameter optional, so the drop
//     runs without a dialog.
type displaySlotRule struct {
	verb                StatementVerb
	needsRowID          bool
	needsRowIDs         bool
	needsOrderedRowIDs  bool
	allowsOrderedRowIDs bool
	othersOptional      bool
	needsPromptParam    bool
	needsTargetParam    bool
}

var displaySlotRules = map[DisplaySlot]displaySlotRule{
	DisplaySlotAdd: {verb: StatementVerbInsert},
	// Toggle pins no verb: flipping a row's done state often records the flip elsewhere — inserting
	// the row the check stands for, and deleting it again on the way back out — which derives to a
	// non-update verb. The :rowid binding and the all-optional parameter shape already make it a
	// one-tap checkbox; the SQL it runs is the author's, including a destructive one.
	DisplaySlotToggle: {needsRowID: true, othersOptional: true},
	// Edit pins no verb: a full edit of a row with owned children rewrites those children
	// (delete + insert) alongside the parent update, which derives to a non-update verb. The
	// :rowid binding and a form field already make it an edit; the SQL it runs is the author's.
	DisplaySlotEdit:       {needsRowID: true, needsPromptParam: true},
	DisplaySlotDelete:     {verb: StatementVerbDelete, needsRowID: true, othersOptional: true},
	DisplaySlotBulk:       {needsRowIDs: true},
	DisplaySlotRowActions: {needsRowID: true},
	// Reorder pins update: a drop persists new positions and nothing else — an action able to
	// insert or delete rows would make a drag destructive.
	DisplaySlotReorder: {verb: StatementVerbUpdate, needsOrderedRowIDs: true, othersOptional: true},
	// Move pins update for the same reason reorder does: a drop re-homes the row and nothing else.
	DisplaySlotMove: {verb: StatementVerbUpdate, needsRowID: true, needsTargetParam: true, allowsOrderedRowIDs: true},
}

// ValidateDisplaySlotAction checks that the attached formula fits the display slot naming it.
// Common ground for the mutation slots: the action's verb must match the slot's pinned verb (where
// one is pinned), row-bound slots must bind the tapped row as :rowid (add never does — an insert
// targets no existing row), and beyond the reserved bindings only prompt/lookup parameters are
// allowed. Per slot:
//
//   - detail: a view — its row-detail parameter shape is already pinned at attach time.
//   - add: an insert action; prompt/lookup parameters of any optionality.
//   - toggle, delete: one-tap slots — every extra parameter optional. Delete pins its verb; toggle
//     pins none, so a composite recording its flip in another table is fine.
//   - edit: at least one prompt/lookup parameter to build its form; pins no verb, so a composite
//     rewriting a row and its owned children is fine.
//   - bulk entries: any verb; the checked rows as :rowids; extras of any optionality (a dialog
//     opens when values are needed).
//   - row_actions entries: any verb; the tapped row as :rowid; extras of any optionality. An entry
//     repeating the edit, delete, or detail ref is not re-checked here: it only positions that slot.
//   - reorder: an update action binding :ordered_rowids, everything else optional — a drop
//     persists positions, it never opens a dialog.
//   - move: an update action binding :rowid plus exactly one required target parameter, everything
//     else optional — a drop never opens a dialog either. On a board the target declares options
//     (the destination column's value binds to it; the options define the board's columns); on a
//     calendar it is a date parameter without options (the destination day binds to it). It may
//     also declare :ordered_rowids, marked optional: the app binds the destination's post-drop
//     order when it sees every row, and omits it otherwise, so the renumbering statement must
//     tolerate NULL.
//   - add on a calendar: at most one date-typed prompt parameter — the one a day's own "+" fills
//     with that day, so the binding is never ambiguous.
//
// mode is the display mode of the view the slot belongs to, which shapes the move target and the
// calendar add. parentAttached marks the slots of a row-detail view (rendered inside its parent's
// modal), where the app also supplies the parent row the modal is open for as :parent_rowid: the
// add slot must declare it so inserts land in that row's children, the row-bound slots and
// row_actions entries may declare it, and outside that context no slot can — there is no parent
// row to bind.
func ValidateDisplaySlotAction(slot DisplaySlot, mode DisplayMode, action *Formula, parentAttached bool) error {
	if slot == DisplaySlotDetail {
		if action.Kind != FormulaKindView {
			return fmt.Errorf("%w: slot %q: formula %q is not a view", ErrFormulaDisplayInvalid, slot, action.Name)
		}
		return nil
	}
	rule, ok := displaySlotRules[slot]
	if !ok {
		return fmt.Errorf("%w: unknown action slot %q", ErrFormulaDisplayInvalid, slot)
	}
	if action.Kind != FormulaKindAction {
		return fmt.Errorf("%w: slot %q: formula %q is not an action", ErrFormulaDisplayInvalid, slot, action.Name)
	}
	if rule.verb != "" && action.StatementVerb != rule.verb {
		return fmt.Errorf("%w: slot %q requires verb %s, %q derives verb %s",
			ErrFormulaDisplayInvalid, slot, rule.verb, action.Name, action.StatementVerb)
	}

	hasRowID, hasRowIDs, hasOrderedRowIDs, orderedOptional, hasParentRowID := false, false, false, false, false
	promptParams := 0
	for _, p := range action.Params {
		switch p.Name {
		case ReservedParamRowID:
			hasRowID = true
			continue
		case ReservedParamRowIDs:
			hasRowIDs = true
			continue
		case ReservedParamOrderedRowIDs:
			hasOrderedRowIDs = true
			orderedOptional = p.Optional
			continue
		case ReservedParamParentRowID:
			hasParentRowID = true
			continue
		}
		if p.Source != ParamSourcePrompt && p.Source != ParamSourceLookup {
			return fmt.Errorf("%w: slot %q: parameter %q of %q must be prompt- or lookup-sourced",
				ErrFormulaDisplayInvalid, slot, p.Name, action.Name)
		}
		if rule.othersOptional && !p.Optional {
			return fmt.Errorf("%w: slot %q: parameter %q of %q must be optional — the tap supplies only the row",
				ErrFormulaDisplayInvalid, slot, p.Name, action.Name)
		}
		promptParams++
	}

	if rule.needsRowID && !hasRowID {
		return fmt.Errorf("%w: slot %q: action %q must declare the %q selection parameter",
			ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamRowID)
	}
	if !rule.needsRowID && hasRowID {
		if slot == DisplaySlotAdd {
			return fmt.Errorf("%w: slot %q: action %q must not declare %q — an insert targets no existing row"+
				" (a detail's add binds its parent as %q)",
				ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamRowID, ReservedParamParentRowID)
		}
		return fmt.Errorf("%w: slot %q: action %q must not bind a single-row selection",
			ErrFormulaDisplayInvalid, slot, action.Name)
	}
	if rule.needsRowIDs && !hasRowIDs {
		return fmt.Errorf("%w: slot %q: action %q must declare the %q selection parameter",
			ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamRowIDs)
	}
	if !rule.needsRowIDs && hasRowIDs {
		return fmt.Errorf("%w: slot %q: action %q must not bind a bulk selection",
			ErrFormulaDisplayInvalid, slot, action.Name)
	}
	if rule.needsOrderedRowIDs && !hasOrderedRowIDs {
		return fmt.Errorf("%w: slot %q: action %q must declare the %q parameter (source %q) — the drop"+
			" supplies every listed rowid in its new order",
			ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamOrderedRowIDs, ParamSourceReorder)
	}
	if !rule.needsOrderedRowIDs && hasOrderedRowIDs {
		if !rule.allowsOrderedRowIDs {
			return fmt.Errorf("%w: slot %q: action %q must not bind the row order — only the reorder and move"+
				" slots supply it", ErrFormulaDisplayInvalid, slot, action.Name)
		}
		if !orderedOptional {
			return fmt.Errorf("%w: slot %q: action %q must mark %q optional — the drop omits it whenever the"+
				" visible rows are a subset, and the SQL must no-op on the NULL",
				ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamOrderedRowIDs)
		}
	}
	if slot == DisplaySlotAdd && parentAttached && !hasParentRowID {
		return fmt.Errorf("%w: slot %q: action %q must declare the %q selection parameter — a detail's add"+
			" inserts children of the row its modal is open for",
			ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamParentRowID)
	}
	if hasParentRowID && !parentAttached {
		return fmt.Errorf("%w: slot %q: action %q declares %q, which only a detail view's slotted action receives",
			ErrFormulaDisplayInvalid, slot, action.Name, ReservedParamParentRowID)
	}
	if rule.needsPromptParam && promptParams == 0 {
		return fmt.Errorf("%w: slot %q: action %q must declare at least one prompt or lookup parameter",
			ErrFormulaDisplayInvalid, slot, action.Name)
	}
	if rule.needsTargetParam {
		if err := validateMoveTargetShape(slot, mode, action); err != nil {
			return err
		}
	}
	if slot == DisplaySlotAdd && mode == DisplayModeCalendar {
		dateParams := 0
		for _, p := range action.Params {
			if p.Source == ParamSourcePrompt && p.Type == ParamTypeDate {
				dateParams++
			}
		}
		if dateParams > 1 {
			return fmt.Errorf("%w: slot %q: action %q declares %d date parameters — a calendar's add may carry"+
				" at most one, the one a day's \"+\" fills", ErrFormulaDisplayInvalid, slot, action.Name, dateParams)
		}
	}
	return nil
}

// validateMoveTargetShape checks a move-slot action's parameter shape beyond the reserved
// bindings (:rowid / :ordered_rowids / :parent_rowid, all skipped here): exactly one required
// parameter — the target the drop binds the destination's value to — and every other prompt/lookup
// parameter optional, so the drop runs immediately without opening a dialog. What the target must
// look like depends on the mode: on a board it declares options, which also define the board's
// columns; on a calendar it is a date parameter without options, the day the drop lands on.
func validateMoveTargetShape(slot DisplaySlot, mode DisplayMode, action *Formula) error {
	requiredCount := 0
	for _, p := range action.Params {
		switch p.Name {
		case ReservedParamRowID, ReservedParamRowIDs, ReservedParamParentRowID, ReservedParamOrderedRowIDs:
			continue
		}
		if p.Optional {
			continue
		}
		requiredCount++
		if mode == DisplayModeCalendar {
			if err := validateCalendarMoveTarget(slot, action, p); err != nil {
				return err
			}
			continue
		}
		if len(p.Options) == 0 {
			return fmt.Errorf("%w: slot %q: action %q must declare options on its required parameter %q —"+
				" the drop binds the destination column's value to it",
				ErrFormulaDisplayInvalid, slot, action.Name, p.Name)
		}
	}

	if requiredCount != 1 {
		return fmt.Errorf("%w: slot %q: action %q must declare exactly one required parameter beyond the reserved"+
			" bindings — the target the drop binds the destination's value to",
			ErrFormulaDisplayInvalid, slot, action.Name)
	}

	return nil
}

// validateCalendarMoveTarget checks that p, a move-slot action's required parameter, can be the target a
// calendar drop binds the destination day to: a date parameter — the day arrives as "YYYY-MM-DD" — that
// declares no options, since a day is never one of a fixed list. A datetime target is refused on purpose:
// a drop knows the day it landed on and nothing about a time, so the app would have to invent one; the
// SQL keeps a datetime column's time itself (`SET starts_at = :day || substr(starts_at, 11)`). The error
// names the slot, the action, and the parameter, wrapping ErrFormulaDisplayInvalid like every other
// display rule.
func validateCalendarMoveTarget(slot DisplaySlot, action *Formula, p FormulaParam) error {
	if p.Type != ParamTypeDate {
		return fmt.Errorf("%w: slot %q: action %q parameter %q must be a date — the drop binds the destination day"+
			" to it", ErrFormulaDisplayInvalid, slot, action.Name, p.Name)
	}
	if len(p.Options) > 0 {
		return fmt.Errorf("%w: slot %q: action %q parameter %q must not declare options — a day is never one of a"+
			" fixed list", ErrFormulaDisplayInvalid, slot, action.Name, p.Name)
	}
	return nil
}
