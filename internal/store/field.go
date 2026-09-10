package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Special key field identifiers. Emarsys addresses contacts by a numeric field
// id, but also accepts the two internal identifiers, so they get negative
// sentinel values that can never collide with a real field id.
const (
	KeyFieldInternalID = -1
	KeyFieldUID        = -2
)

// FieldDef is one entry in the contact field catalogue.
type FieldDef struct {
	ID              int
	StringID        string
	Name            string
	ApplicationType string
	IsSystem        bool
	IsIndexed       bool
}

// Choice is one option of a single- or multi-choice field.
type Choice struct {
	ID    int
	Label string
	Sort  int
}

// FieldCatalog is an immutable snapshot of the field definitions, loaded once
// per request. Handlers validate every incoming field id against it, which is
// what turns a typo in an integration into replyCode 2006 rather than a silent
// write to a column nobody reads.
type FieldCatalog struct {
	byID    map[int]FieldDef
	choices map[int][]Choice
	order   []int
}

func (db *DB) LoadFieldCatalog(ctx context.Context) (*FieldCatalog, error) {
	rows, err := db.Read.QueryContext(ctx,
		`SELECT id, string_id, name, application_type, is_system, is_indexed FROM fields ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load fields: %w", err)
	}
	defer rows.Close()

	cat := &FieldCatalog{byID: map[int]FieldDef{}, choices: map[int][]Choice{}}
	for rows.Next() {
		var f FieldDef
		if err := rows.Scan(&f.ID, &f.StringID, &f.Name, &f.ApplicationType, &f.IsSystem, &f.IsIndexed); err != nil {
			return nil, err
		}
		cat.byID[f.ID] = f
		cat.order = append(cat.order, f.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	choiceRows, err := db.Read.QueryContext(ctx,
		`SELECT field_id, choice_id, label, sort_id FROM field_choices ORDER BY field_id, sort_id, choice_id`)
	if err != nil {
		return nil, fmt.Errorf("load field choices: %w", err)
	}
	defer choiceRows.Close()
	for choiceRows.Next() {
		var fieldID int
		var c Choice
		if err := choiceRows.Scan(&fieldID, &c.ID, &c.Label, &c.Sort); err != nil {
			return nil, err
		}
		cat.choices[fieldID] = append(cat.choices[fieldID], c)
	}
	return cat, choiceRows.Err()
}

func (c *FieldCatalog) Get(id int) (FieldDef, bool) {
	f, ok := c.byID[id]
	return f, ok
}

func (c *FieldCatalog) Has(id int) bool {
	if id == KeyFieldInternalID || id == KeyFieldUID {
		return true
	}
	_, ok := c.byID[id]
	return ok
}

// All returns the catalogue in field-id order.
func (c *FieldCatalog) All() []FieldDef {
	out := make([]FieldDef, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.byID[id])
	}
	return out
}

func (c *FieldCatalog) Choices(fieldID int) []Choice { return c.choices[fieldID] }

// HasChoice reports whether a value is one of a choice field's options.
func (c *FieldCatalog) HasChoice(fieldID int, value string) bool {
	for _, ch := range c.choices[fieldID] {
		if fmt.Sprint(ch.ID) == value {
			return true
		}
	}
	return false
}

// ErrSystemField is returned when a caller tries to delete a system field.
var ErrSystemField = errors.New("system fields cannot be deleted")

// ErrNoField is returned when a field id does not exist.
var ErrNoField = errors.New("no such field")

// CreateField adds a custom field and returns its assigned id.
//
// Emarsys assigns the id itself; the mock uses the next free one so that seeded
// system ids stay where integrations expect them.
func (db *DB) CreateField(ctx context.Context, name, applicationType, stringID string, indexed bool) (int, error) {
	if stringID == "" {
		stringID = slugify(name)
	}
	var next int
	if err := db.Write.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) + 1 FROM fields`).Scan(&next); err != nil {
		return 0, fmt.Errorf("allocate field id: %w", err)
	}
	_, err := db.Write.ExecContext(ctx,
		`INSERT INTO fields (id, string_id, name, application_type, is_system, is_indexed, created_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?)`,
		next, stringID, name, applicationType, indexed, nowString())
	if err != nil {
		return 0, fmt.Errorf("create field: %w", err)
	}
	return next, nil
}

// DeleteField removes a custom field and every stored value for it.
func (db *DB) DeleteField(ctx context.Context, id int) error {
	var isSystem int
	err := db.Write.QueryRowContext(ctx, `SELECT is_system FROM fields WHERE id = ?`, id).Scan(&isSystem)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoField
	}
	if err != nil {
		return err
	}
	if isSystem == 1 {
		return ErrSystemField
	}
	// contact_values cascades on the foreign key; history is kept on purpose so
	// an audit trail survives a field being removed.
	_, err = db.Write.ExecContext(ctx, `DELETE FROM fields WHERE id = ?`, id)
	return err
}

// SetFieldIndexed toggles whether contact/query accepts the field.
func (db *DB) SetFieldIndexed(ctx context.Context, id int, indexed bool) error {
	res, err := db.Write.ExecContext(ctx, `UPDATE fields SET is_indexed = ? WHERE id = ?`, indexed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoField
	}
	return nil
}

// AddChoice adds an option to a choice field.
func (db *DB) AddChoice(ctx context.Context, fieldID, choiceID int, label string, sort int) error {
	_, err := db.Write.ExecContext(ctx,
		`INSERT OR REPLACE INTO field_choices (field_id, choice_id, label, sort_id) VALUES (?, ?, ?, ?)`,
		fieldID, choiceID, label, sort)
	return err
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339) }

// slugify derives a string_id from a field name the way Emarsys does for
// generated identifiers: lower case, non-alphanumerics collapsed to underscores.
func slugify(name string) string {
	out := make([]rune, 0, len(name))
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
			lastUnderscore = false
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
			lastUnderscore = false
		default:
			if !lastUnderscore && len(out) > 0 {
				out = append(out, '_')
				lastUnderscore = true
			}
		}
	}
	s := string(out)
	for len(s) > 0 && s[len(s)-1] == '_' {
		s = s[:len(s)-1]
	}
	if s == "" {
		s = "field"
	}
	return s
}

// sortedFieldIDs is a small helper for deterministic iteration over a value map.
func sortedFieldIDs(values map[int]string) []int {
	ids := make([]int, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}
