package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// ContextBudgetBytes is the default size of the session-start digest.
	ContextBudgetBytes = 2048

	// omittedFooterReserve is kept free so the "N more" footer always fits.
	omittedFooterReserve = 40
)

var (
	ErrNotFound         = errors.New("not found")
	ErrNoChange         = errors.New("nothing to update")
	ErrDuplicateContent = errors.New("another memory in this project already has this exact title and body")
)

// Version is a prior snapshot of a memory.
type Version struct {
	Version int
	Title   string
	Body    string
	SavedAt string
}

// Memory is a full stored memory with its version history.
type Memory struct {
	ID           int64
	Project      string
	Scope        string
	Type         string
	Title        string
	Body         string
	TopicKey     string
	Pinned       bool
	Status       string
	SupersededBy int64
	UseCount     int
	CreatedAt    string
	UpdatedAt    string
	History      []Version
}

// Get returns one memory with its history and records the read in use_count.
func (s *Service) Get(ctx context.Context, id int64) (Memory, error) {
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	r, err := s.db.SQL().ExecContext(ctx,
		`update memories set use_count = use_count + 1, last_used_at = ? where id = ?`, stamp, id)
	if err != nil {
		return Memory{}, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return Memory{}, ErrNotFound
	}
	var m Memory
	var topic sql.NullString
	var sup sql.NullInt64
	var pinned int
	err = s.db.SQL().QueryRowContext(ctx,
		`select id, project, scope, type, title, body, topic_key, pinned, status, superseded_by,
		        use_count, created_at, updated_at from memories where id = ?`, id).
		Scan(&m.ID, &m.Project, &m.Scope, &m.Type, &m.Title, &m.Body, &topic, &pinned, &m.Status, &sup,
			&m.UseCount, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return Memory{}, err
	}
	m.TopicKey, m.Pinned, m.SupersededBy = topic.String, pinned == 1, sup.Int64

	rows, err := s.db.SQL().QueryContext(ctx,
		`select version, title, body, saved_at from memory_versions where memory_id = ? order by version`, id)
	if err != nil {
		return Memory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.Title, &v.Body, &v.SavedAt); err != nil {
			return Memory{}, err
		}
		m.History = append(m.History, v)
	}
	return m, rows.Err()
}

// UpdateInput lists the changes to apply; nil or false fields are left alone.
type UpdateInput struct {
	Title    *string
	Body     *string
	Pinned   *bool
	Archive  *bool // true archives (soft delete), false restores an archived memory
	Reviewed bool  // refresh updated_at so a stale flag clears, without a new version
}

// Update edits a memory by id. Edits follow the save rules (redaction, caps,
// version snapshot, re-embedding); the other changes touch only metadata.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (SaveResult, error) {
	edit := in.Title != nil || in.Body != nil
	if !edit && in.Pinned == nil && in.Archive == nil && !in.Reviewed {
		return SaveResult{}, ErrNoChange
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback()

	var cur SaveInput
	var oldHash string
	var topic sql.NullString
	err = tx.QueryRowContext(ctx,
		`select project, scope, type, title, body, topic_key, content_hash from memories where id = ?`, id).
		Scan(&cur.Project, &cur.Scope, &cur.Type, &cur.Title, &cur.Body, &topic, &oldHash)
	if errors.Is(err, sql.ErrNoRows) {
		return SaveResult{}, ErrNotFound
	}
	if err != nil {
		return SaveResult{}, err
	}
	cur.TopicKey = topic.String
	res := SaveResult{ID: id, Action: ActionUnchanged}

	reembed := false
	if edit {
		next := cur
		if in.Title != nil {
			next.Title = *in.Title
		}
		if in.Body != nil {
			next.Body = *in.Body
		}
		if next, err = normalize(next); err != nil {
			return SaveResult{}, err
		}
		hash := contentHash(next.Title, next.Body)
		if hash != oldHash {
			var other int64
			err := tx.QueryRowContext(ctx,
				`select id from memories where project = ? and content_hash = ? and id != ? limit 1`,
				next.Project, hash, id).Scan(&other)
			if err == nil {
				return SaveResult{}, fmt.Errorf("%w (#%d)", ErrDuplicateContent, other)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return SaveResult{}, err
			}
			if err := s.update(ctx, tx, id, cur.Title, cur.Body, next, hash, stamp); err != nil {
				return SaveResult{}, err
			}
			res.Action, reembed = ActionUpdated, true
			cur = next
		}
	}
	if in.Pinned != nil {
		if _, err := tx.ExecContext(ctx, `update memories set pinned = ? where id = ?`, b2i(*in.Pinned), id); err != nil {
			return SaveResult{}, err
		}
		res.Action = ActionUpdated
	}
	if in.Archive != nil {
		q := `update memories set status = 'archived' where id = ? and status = 'active'`
		if !*in.Archive {
			q = `update memories set status = 'active' where id = ? and status = 'archived'`
		}
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return SaveResult{}, err
		}
		res.Action = ActionUpdated
	}
	if in.Reviewed {
		if _, err := tx.ExecContext(ctx, `update memories set updated_at = ? where id = ?`, stamp, id); err != nil {
			return SaveResult{}, err
		}
		res.Action = ActionUpdated
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	if reembed {
		s.afterWrite(ctx, id, cur.Project, cur.Title, cur.Body, ActionUpdated, &res)
	}
	return res, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Digest groups.
const (
	GroupPinned = "pinned"
	GroupStale  = "stale"
	GroupRecent = "recent"
)

// DigestEntry is one line of the session-start digest.
type DigestEntry struct {
	ID    int64
	Type  string
	Title string
	Group string
}

// Digest is a small, budgeted summary of a project's memory.
type Digest struct {
	Entries []DigestEntry
	Omitted int // active memories that did not fit the budget
}

// Text renders the digest, one line per entry.
func (d Digest) Text() string {
	var b strings.Builder
	for _, e := range d.Entries {
		b.WriteString(e.line())
	}
	if d.Omitted > 0 {
		fmt.Fprintf(&b, "... %d more (use mem_search)\n", d.Omitted)
	}
	return b.String()
}

func (e DigestEntry) line() string {
	return fmt.Sprintf("#%d [%s] %s: %s\n", e.ID, e.Group, e.Type, e.Title)
}

// Context builds the digest for a project: pinned first, then stale decisions
// that need review, then the most recent, cut at budget bytes.
func (s *Service) Context(ctx context.Context, project string, budget int) (Digest, error) {
	if budget <= 0 {
		budget = ContextBudgetBytes
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select id, type, title, pinned, updated_at from memories
		 where status = 'active' and (project = ? or scope = 'global')
		 order by updated_at desc, id desc`, project)
	if err != nil {
		return Digest{}, err
	}
	defer rows.Close()

	now := s.now()
	var pinned, stale, recent []DigestEntry
	for rows.Next() {
		var e DigestEntry
		var pin int
		var updated string
		if err := rows.Scan(&e.ID, &e.Type, &e.Title, &pin, &updated); err != nil {
			return Digest{}, err
		}
		switch {
		case pin == 1:
			e.Group = GroupPinned
			pinned = append(pinned, e)
		case isStale(e.Type, updated, now):
			e.Group = GroupStale
			stale = append(stale, e)
		default:
			e.Group = GroupRecent
			recent = append(recent, e)
		}
	}
	if err := rows.Err(); err != nil {
		return Digest{}, err
	}

	all := append(append(pinned, stale...), recent...)
	var d Digest
	used := 0
	for i, e := range all {
		n := len(e.line())
		if used+n > budget-omittedFooterReserve && i < len(all) {
			d.Omitted = len(all) - i
			break
		}
		d.Entries = append(d.Entries, e)
		used += n
	}
	return d, nil
}

// SessionStart registers a session; starting an existing one is a no-op.
func (s *Service) SessionStart(ctx context.Context, id, project string) error {
	id, project = strings.TrimSpace(id), strings.TrimSpace(project)
	if id == "" {
		return errors.New("session id is required")
	}
	if project == "" {
		return ErrNoProject
	}
	_, err := s.db.SQL().ExecContext(ctx,
		`insert or ignore into sessions(id, project, started_at) values (?,?,?)`,
		id, project, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

// SessionEnd marks a session finished.
func (s *Service) SessionEnd(ctx context.Context, id string) error {
	r, err := s.db.SQL().ExecContext(ctx,
		`update sessions set ended_at = ? where id = ?`, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionSummary saves the session's summary under the fixed topic key
// session/<id>, so a second call updates the first instead of piling up.
func (s *Service) SessionSummary(ctx context.Context, id, text string) (SaveResult, error) {
	var project string
	err := s.db.SQL().QueryRowContext(ctx, `select project from sessions where id = ?`, id).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return SaveResult{}, ErrNotFound
	}
	if err != nil {
		return SaveResult{}, err
	}
	return s.Save(ctx, SaveInput{
		Project:  project,
		Type:     "session_summary",
		Title:    "Session " + id + " summary",
		Body:     text,
		TopicKey: "session/" + id,
	})
}
