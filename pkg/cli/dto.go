package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// bdbJSON is a bdb as --json prints it.
type bdbJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
}

// bdbSummaryJSON is a bdb in a listing as --json prints it. Tables is null and TablesError set when the bdb's tables
// could not be read.
type bdbSummaryJSON struct {
	bdbJSON
	Tables      []apptypes.TableInfo `json:"tables"`
	TablesError string               `json:"tables_error,omitempty"`
}

// createdBdbJSON is a bdb just created as --json prints it: the bdb, the starter it was seeded from (absent for a
// blank bdb), and its tables and formulas, null with ContentsError set when they could not be read back.
type createdBdbJSON struct {
	bdbJSON
	StarterKey    string               `json:"starter_key,omitempty"`
	Tables        []apptypes.TableInfo `json:"tables"`
	Formulas      []apptypes.Formula   `json:"formulas"`
	ContentsError string               `json:"contents_error,omitempty"`
}

// toCreatedBdbJSON converts c to its JSON shape, with empty arrays rather than null when its contents were read.
func toCreatedBdbJSON(c domain.CreatedBdb) createdBdbJSON {
	out := createdBdbJSON{bdbJSON: toBdbJSON(c.Bdb), StarterKey: c.StarterKey, Tables: c.Tables, Formulas: c.Formulas}
	if c.ContentsErr != nil {
		out.ContentsError = c.ContentsErr.Error()
		return out
	}
	if out.Tables == nil {
		out.Tables = []apptypes.TableInfo{}
	}
	if out.Formulas == nil {
		out.Formulas = []apptypes.Formula{}
	}
	return out
}

// toBdbJSON converts b to its JSON shape.
func toBdbJSON(b domain.Bdb) bdbJSON {
	return bdbJSON{ID: b.ID.String(), Name: b.Name, Description: b.Description, Role: b.Role}
}

// toBdbSummariesJSON converts summaries to their JSON shape, an empty array rather than null when there are none.
func toBdbSummariesJSON(summaries []domain.BdbSummary) []bdbSummaryJSON {
	out := make([]bdbSummaryJSON, len(summaries))
	for i, s := range summaries {
		out[i] = bdbSummaryJSON{bdbJSON: toBdbJSON(s.Bdb), Tables: s.Tables}
		switch {
		case s.TablesErr != nil:
			out[i].Tables, out[i].TablesError = nil, s.TablesErr.Error()
		case s.Tables == nil:
			out[i].Tables = []apptypes.TableInfo{}
		}
	}
	return out
}

// starterJSON is a starter as --json prints it.
type starterJSON struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// toStartersJSON converts starters to their JSON shape, with an empty array rather than null for no starters or no
// tags.
func toStartersJSON(starters []domain.Starter) []starterJSON {
	out := make([]starterJSON, len(starters))
	for i, s := range starters {
		out[i] = starterJSON{Key: s.Key, Name: s.Name, Description: s.Description, Tags: s.Tags}
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
	return out
}

// rowJSON is a result row printed as a JSON object whose keys follow the result's column order. Blob values print
// as base64 strings.
type rowJSON struct {
	columns []string
	values  []any
}

// MarshalJSON writes the row as an object, one key per column in order.
func (r rowJSON) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, column := range r.columns {
		key, err := marshalPlain(column)
		if err != nil {
			return nil, err
		}
		value, err := marshalPlain(r.values[i])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalPlain returns v encoded as JSON, as json.Marshal does but keeping <, > and & as they are, as writeJSON does.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// toRowsJSON converts r's rows to JSON objects, an empty array rather than null when there are none. A result naming
// the same column twice is an error: a row object would keep only one of the two values.
func toRowsJSON(r domain.QueryResult) ([]rowJSON, error) {
	names := make([]string, len(r.Columns))
	seen := make(map[string]bool, len(r.Columns))
	for i, c := range r.Columns {
		if seen[c.Name] {
			return nil, fmt.Errorf("column %q appears twice in the result; give one an alias (... AS another_name) "+
				"to print it as JSON", c.Name)
		}
		seen[c.Name] = true
		names[i] = c.Name
	}
	rows := make([]rowJSON, len(r.Rows))
	for i, values := range r.Rows {
		rows[i] = rowJSON{columns: names, values: values}
	}
	return rows, nil
}

// changeResultJSON is the result of a data or schema change as --json prints it: the rows a RETURNING clause
// returned, how many rows changed, and the changed rows with their values before and after.
type changeResultJSON struct {
	Rows                  []rowJSON         `json:"rows"`
	AffectedRowCount      int64             `json:"affected_row_count"`
	LastInsertRowID       *int64            `json:"last_insert_row_id,omitempty"`
	BatchID               string            `json:"batch_id,omitempty"`
	AffectedRows          []affectedRowJSON `json:"affected_rows,omitempty"`
	AffectedRowsTruncated bool              `json:"affected_rows_truncated,omitempty"`
}

// affectedRowJSON is a row a change touched as --json prints it.
type affectedRowJSON struct {
	TableName string         `json:"table_name"`
	RowID     string         `json:"row_id"`
	Operation string         `json:"operation"`
	OldValues map[string]any `json:"old_values,omitempty"`
	NewValues map[string]any `json:"new_values,omitempty"`
}

// toChangeResultJSON converts the result of a data or schema change to its JSON shape, failing as toRowsJSON does.
func toChangeResultJSON(r domain.QueryResult) (changeResultJSON, error) {
	rows, err := toRowsJSON(r)
	if err != nil {
		return changeResultJSON{}, err
	}
	out := changeResultJSON{
		Rows:                  rows,
		AffectedRowCount:      r.AffectedRowCount,
		LastInsertRowID:       r.LastInsertRowID,
		BatchID:               r.BatchID,
		AffectedRowsTruncated: r.AffectedRowsTruncated,
	}
	for _, ar := range r.AffectedRows {
		out.AffectedRows = append(out.AffectedRows, affectedRowJSON{
			TableName: ar.TableName, RowID: ar.RowID, Operation: ar.Operation,
			OldValues: ar.OldValues, NewValues: ar.NewValues,
		})
	}
	return out, nil
}

// changeBatchJSON is a change batch as --json prints it.
type changeBatchJSON struct {
	BatchID   string            `json:"batch_id"`
	Kind      string            `json:"kind"`
	Origin    string            `json:"origin"`
	ChangedBy string            `json:"changed_by"`
	Mine      bool              `json:"mine"`
	ChangedAt string            `json:"changed_at"`
	Entries   []changeEntryJSON `json:"entries"`
	Partial   bool              `json:"partial,omitempty"`
}

// changeEntryJSON is a change log entry as --json prints it.
type changeEntryJSON struct {
	ID         int64          `json:"id"`
	Operation  string         `json:"operation"`
	Table      string         `json:"table"`
	ObjectType string         `json:"object_type,omitempty"`
	RowID      string         `json:"row_id,omitempty"`
	OldValues  map[string]any `json:"old_values,omitempty"`
	NewValues  map[string]any `json:"new_values,omitempty"`
	Statement  string         `json:"statement,omitempty"`
}

// toChangeBatchesJSON converts batches to their JSON shape, an empty array rather than null when there are none, with
// times in RFC 3339.
func toChangeBatchesJSON(batches []domain.ChangeBatch) []changeBatchJSON {
	out := make([]changeBatchJSON, len(batches))
	for i, b := range batches {
		out[i] = changeBatchJSON{
			BatchID: b.ID, Kind: b.Kind, Origin: b.Origin, ChangedBy: b.ChangedBy, Mine: b.Mine,
			ChangedAt: b.ChangedAt.Format(time.RFC3339), Entries: make([]changeEntryJSON, len(b.Entries)),
			Partial: b.Partial,
		}
		for j, e := range b.Entries {
			out[i].Entries[j] = changeEntryJSON{
				ID: e.ID, Operation: e.Operation, Table: e.Table, ObjectType: e.ObjectType, RowID: e.RowID,
				OldValues: e.OldValues, NewValues: e.NewValues, Statement: e.Statement,
			}
		}
	}
	return out
}

// exportJSON is a bdb export as --json prints it: the passphrase, or the file holding it.
type exportJSON struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	Passphrase     string `json:"passphrase,omitempty"`
	PassphraseFile string `json:"passphrase_file,omitempty"`
}

// formulaDetailJSON is a formula as formulas show --json prints it: what running it takes, without the app's display
// configuration.
type formulaDetailJSON struct {
	Name        string             `json:"name"`
	Label       string             `json:"label,omitempty"`
	Description string             `json:"description"`
	Kind        string             `json:"kind"`
	Verb        string             `json:"verb"`
	Hidden      bool               `json:"hidden,omitempty"`
	SQL         string             `json:"sql"`
	Params      []formulaParamJSON `json:"params"`
}

// formulaParamJSON is a formula's parameter as --json prints it.
type formulaParamJSON struct {
	Name     string                `json:"name"`
	Type     string                `json:"type"`
	Source   string                `json:"source"`
	Required bool                  `json:"required"`
	Default  string                `json:"default,omitempty"`
	Defaults []string              `json:"defaults,omitempty"`
	Options  []formulaOptionJSON   `json:"options,omitempty"`
	Multiple bool                  `json:"multiple,omitempty"`
	Lookup   *formulaLookupJSON    `json:"lookup,omitempty"`
	When     *formulaParamWhenJSON `json:"when,omitempty"`
	Fields   []formulaParamJSON    `json:"fields,omitempty"`
}

// formulaOptionJSON is one value a parameter accepts as --json prints it.
type formulaOptionJSON struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// formulaLookupJSON is where a lookup parameter's values come from as --json prints it.
type formulaLookupJSON struct {
	Table       string `json:"table"`
	ValueColumn string `json:"value_column"`
	LabelColumn string `json:"label_column"`
	AllowNew    bool   `json:"allow_new,omitempty"`
}

// formulaParamWhenJSON is a parameter's condition as --json prints it.
type formulaParamWhenJSON struct {
	Param string `json:"param"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// toFormulaDetailJSON converts f to its JSON shape, an empty params array rather than null when it has none.
func toFormulaDetailJSON(f domain.FormulaDetail) formulaDetailJSON {
	params := toFormulaParamsJSON(f.Params)
	if params == nil {
		params = []formulaParamJSON{}
	}
	return formulaDetailJSON{
		Name: f.Name, Label: f.Label, Description: f.Description, Kind: f.Kind, Verb: f.Verb, Hidden: f.Hidden,
		SQL: f.SQL, Params: params,
	}
}

// toFormulaParamsJSON converts params to their JSON shape, nil when there are none.
func toFormulaParamsJSON(params []domain.FormulaParam) []formulaParamJSON {
	if len(params) == 0 {
		return nil
	}
	out := make([]formulaParamJSON, len(params))
	for i, p := range params {
		out[i] = formulaParamJSON{
			Name: p.Name, Type: p.Type, Source: p.Source, Required: p.Required, Default: p.Default,
			Defaults: p.Defaults, Multiple: p.Multiple, Fields: toFormulaParamsJSON(p.Fields),
		}
		for _, o := range p.Options {
			out[i].Options = append(out[i].Options, formulaOptionJSON(o))
		}
		if p.Lookup != nil {
			out[i].Lookup = (*formulaLookupJSON)(p.Lookup)
		}
		if p.When != nil {
			out[i].When = &formulaParamWhenJSON{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
		}
	}
	return out
}

// userJSON is an account as --json prints it.
type userJSON struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Admin     bool   `json:"admin"`
	CreatedAt string `json:"created_at"`
}

// toUserJSON converts u to its JSON shape, its creation time in RFC 3339.
func toUserJSON(u domain.User) userJSON {
	return userJSON{
		ID: u.ID.String(), Email: u.Email, FirstName: u.FirstName, LastName: u.LastName, Admin: u.Admin,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
}

// toUsersJSON converts users to their JSON shape, an empty array rather than null when there are none.
func toUsersJSON(users []domain.User) []userJSON {
	out := make([]userJSON, len(users))
	for i, u := range users {
		out[i] = toUserJSON(u)
	}
	return out
}

// accessTokenJSON is a personal access token as --json prints it, never with its value. LastUsedAt is null for a
// token never used.
type accessTokenJSON struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
}

// toAccessTokensJSON converts tokens to their JSON shape, times in RFC 3339, an empty array rather than null when
// there are none.
func toAccessTokensJSON(tokens []domain.AccessToken) []accessTokenJSON {
	out := make([]accessTokenJSON, len(tokens))
	for i, t := range tokens {
		out[i] = accessTokenJSON{
			ID: t.ID.String(), Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt.Format(time.RFC3339),
		}
		if t.LastUsedAt != nil {
			lastUsed := t.LastUsedAt.Format(time.RFC3339)
			out[i].LastUsedAt = &lastUsed
		}
		if t.ExpiresAt != nil {
			expires := t.ExpiresAt.Format(time.RFC3339)
			out[i].ExpiresAt = &expires
		}
	}
	return out
}

// infoJSON is a domain.Info as info --json prints it.
type infoJSON struct {
	Binary   binaryInfoJSON      `json:"binary"`
	DataDir  dataDirInfoJSON     `json:"data_dir"`
	Instance *instanceInfoJSON   `json:"instance"`
	As       string              `json:"as,omitempty"`
	Server   *servedInstanceJSON `json:"server"`
	MCPName  string              `json:"mcp_name"`
}

// binaryInfoJSON is a domain.BinaryInfo as info --json prints it.
type binaryInfoJSON struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CipherVersion  string `json:"cipher_version"`
	CipherProvider string `json:"cipher_provider"`
	WebApp         bool   `json:"web_app"`
}

// dataDirInfoJSON is a domain.DataDirInfo as info --json prints it.
type dataDirInfoJSON struct {
	Path               string       `json:"path"`
	Source             string       `json:"source"`
	Exists             bool         `json:"exists"`
	Key                dataFileJSON `json:"key"`
	InstanceStore      dataFileJSON `json:"instance_store"`
	Databases          dataFileJSON `json:"databases"`
	KeyOwnerOnly       bool         `json:"key_owner_only"`
	InstanceStoreBytes int64        `json:"instance_store_bytes"`
	BdbFiles           int          `json:"bdb_files"`
	BdbBytes           int64        `json:"bdb_bytes"`
}

// dataFileJSON is a domain.DataFile as info --json prints it.
type dataFileJSON struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Override string `json:"override,omitempty"`
}

// instanceInfoJSON is a domain.InstanceSummary as info --json prints it.
type instanceInfoJSON struct {
	Owner          *ownerJSON      `json:"owner"`
	AsFound        *bool           `json:"as_found,omitempty"`
	Bdbs           int             `json:"bdbs"`
	Accounts       int             `json:"accounts"`
	Schemas        []appSchemaJSON `json:"schemas"`
	PlatformSchema int             `json:"platform_schema"`
}

// ownerJSON is the owner as info --json prints it.
type ownerJSON struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// appSchemaJSON is a domain.AppSchema as info --json prints it.
type appSchemaJSON struct {
	App     string `json:"app"`
	Version uint   `json:"version"`
	Latest  uint   `json:"latest"`
	Dirty   bool   `json:"dirty"`
}

// servedInstanceJSON is a domain.ServedInstance as info --json prints it.
type servedInstanceJSON struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

// toInfoJSON converts info, the account check included only when --as was given.
func toInfoJSON(info domain.Info) infoJSON {
	d := info.DataDir
	out := infoJSON{
		Binary: binaryInfoJSON(info.Binary),
		DataDir: dataDirInfoJSON{
			Path: d.Path, Source: d.Source, Exists: d.Exists,
			Key: dataFileJSON(d.Key), InstanceStore: dataFileJSON(d.InstanceStore),
			Databases:    dataFileJSON(d.Databases),
			KeyOwnerOnly: d.KeyOwnerOnly, InstanceStoreBytes: d.InstanceStoreBytes, BdbFiles: d.BdbFiles,
			BdbBytes: d.BdbBytes,
		},
		As:      info.As,
		MCPName: info.MCPName,
	}
	if in := info.Instance; in != nil {
		inst := &instanceInfoJSON{Bdbs: in.Bdbs, Accounts: in.Accounts, PlatformSchema: in.PlatformSchema,
			Schemas: make([]appSchemaJSON, len(in.Schemas))}
		if in.Owner != nil {
			inst.Owner = &ownerJSON{Email: in.Owner.Email, Name: fullName(*in.Owner)}
		}
		if info.As != "" {
			inst.AsFound = &in.AsFound
		}
		for i, s := range in.Schemas {
			inst.Schemas[i] = appSchemaJSON{App: s.App, Version: s.Version, Latest: s.Latest, Dirty: s.Dirty}
		}
		out.Instance = inst
	}
	if info.Server != nil {
		out.Server = &servedInstanceJSON{URL: info.Server.URL, PID: info.Server.PID}
	}
	return out
}
