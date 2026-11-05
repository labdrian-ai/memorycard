package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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
		// Archiving records its time in updated_at, so a later save can tell
		// whether it is newer. Restoring keeps it: a restore is no review, and
		// must not clear a stale flag.
		q, args := `update memories set status = 'archived', updated_at = ? where id = ? and status = 'active'`, []any{stamp, id}
		if !*in.Archive {
			q, args = `update memories set status = 'active' where id = ? and status = 'archived'`, []any{id}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
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

const (
	// DigestStaleLines caps the stale tier; the rest is counted on one line.
	DigestStaleLines = 3

	// DigestHitsK is how many search hits a digest asked for a task appends.
	DigestHitsK = 5
)

// Kinds of recent memory the digest shows as one line: the newest one, with
// a count of the rest.
const (
	kindSessions = "session summaries"
	kindMirrors  = "mirrors"
)

// mirrorPrefixes are the topic-key roots under which the SDD and ODD
// workflows mirror their documents automatically.
var mirrorPrefixes = []string{"sdd/", "odd/", "project/"}

// titledOnlyPrefixes are the roots where architecture notes are also written
// by hand (ADRs, the technical architecture): there only a document titled by
// its topic key counts as a mirror.
var titledOnlyPrefixes = []string{"project/"}

// partTitle matches the title of one part of a document stored in parts.
var partTitle = regexp.MustCompile(`^(.*) \(part (\d+)/(\d+)\)$`)

// DigestEntry is one line of the session-start digest.
type DigestEntry struct {
	ID       int64
	Type     string
	Title    string
	Group    string
	Parts    int    // more than 1 when the document is stored in parts; ID is its first active part
	More     int    // further memories of MoreKind this line stands for
	MoreKind string // "session summaries" or "mirrors"
}

// Digest is a small, budgeted summary of a project's memory.
type Digest struct {
	Entries      []DigestEntry
	StaleOmitted int // stale memories past the stale tier cap
	Omitted      int // digest lines that did not fit the budget
}

// Text renders the digest, one line per entry.
func (d Digest) Text() string {
	var b strings.Builder
	for i, e := range d.Entries {
		b.WriteString(e.line())
		if d.StaleOmitted > 0 && lastStale(d.Entries, i) {
			b.WriteString(staleFooter(d.StaleOmitted))
		}
	}
	if d.Omitted > 0 {
		fmt.Fprintf(&b, "... %d more (use mem_search)\n", d.Omitted)
	}
	return b.String()
}

// lastStale reports whether entry i closes the stale tier.
func lastStale(es []DigestEntry, i int) bool {
	return es[i].Group == GroupStale && (i+1 == len(es) || es[i+1].Group != GroupStale)
}

func staleFooter(n int) string {
	return fmt.Sprintf("... %d more stale (use mem_search)\n", n)
}

func (e DigestEntry) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d [%s] %s: %s", e.ID, e.Group, e.Type, e.Title)
	if e.Parts > 1 {
		fmt.Fprintf(&b, " (%d parts)", e.Parts)
	}
	if e.More > 0 {
		fmt.Fprintf(&b, " (+%d more %s)", e.More, e.MoreKind)
	}
	b.WriteByte('\n')
	return b.String()
}

// Context builds the digest for a project: pinned first, then up to
// DigestStaleLines stale decisions that need review, then the most recent,
// cut at budget bytes. A document stored in parts is one line; session
// summaries and workflow mirrors are one line each kind.
func (s *Service) Context(ctx context.Context, project string, budget int) (Digest, error) {
	if budget <= 0 {
		budget = ContextBudgetBytes
	}
	docs, err := s.digestDocs(ctx, project)
	if err != nil {
		return Digest{}, err
	}

	now := s.now()
	var pinned, stale, recent []DigestEntry
	firstOfKind := map[string]int{} // kind -> index of its line in recent
	for _, doc := range docs {
		e := doc.DigestEntry
		switch {
		case doc.pinned:
			e.Group = GroupPinned
			pinned = append(pinned, e)
		case isStale(e.Type, doc.updated, now):
			e.Group = GroupStale
			stale = append(stale, e)
		default:
			e.Group = GroupRecent
			if kind := doc.kind(); kind != "" {
				if i, ok := firstOfKind[kind]; ok {
					recent[i].More++
					continue
				}
				firstOfKind[kind] = len(recent)
				e.MoreKind = kind
			}
			recent = append(recent, e)
		}
	}

	var d Digest
	if len(stale) > DigestStaleLines {
		d.StaleOmitted = len(stale) - DigestStaleLines
		stale = stale[:DigestStaleLines]
	}
	all := append(append(pinned, stale...), recent...)
	used := 0
	for i, e := range all {
		n := len(e.line())
		if d.StaleOmitted > 0 && lastStale(all, i) {
			n += len(staleFooter(d.StaleOmitted))
		}
		if used+n > budget-omittedFooterReserve {
			d.Omitted = len(all) - i
			if i < len(pinned)+len(stale) {
				// The stale footer was cut too: its count joins the rest.
				d.Omitted += d.StaleOmitted
				d.StaleOmitted = 0
			}
			break
		}
		d.Entries = append(d.Entries, e)
		used += n
	}
	return d, nil
}

// digestDoc is one document of the digest: a memory, or all the active
// parts of a document stored in parts.
type digestDoc struct {
	DigestEntry
	topic   string
	pinned  bool
	updated string // of the most recently updated part
	part    int    // lowest part number seen
}

// kind returns the kind of memory shown as one line, or "".
func (d *digestDoc) kind() string {
	if d.Type == "session_summary" {
		return kindSessions
	}
	titled := strings.HasPrefix(d.Title, d.topic)
	for _, p := range mirrorPrefixes {
		if !strings.HasPrefix(d.topic, p) {
			continue
		}
		if titled || (d.Type == "architecture" && !slices.Contains(titledOnlyPrefixes, p)) {
			return kindMirrors
		}
	}
	return ""
}

// digestDocs lists a project's active documents, most recently updated
// first, with the parts of one document folded into one entry.
func (s *Service) digestDocs(ctx context.Context, project string) ([]*digestDoc, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`select id, type, title, coalesce(topic_key, ''), pinned, updated_at from memories
		 where status = 'active' and (project = ? or scope = 'global')
		 order by updated_at desc, id desc`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []*digestDoc
	byKey := map[string]*digestDoc{}
	for rows.Next() {
		d := &digestDoc{}
		var pin int
		if err := rows.Scan(&d.ID, &d.Type, &d.Title, &d.topic, &pin, &d.updated); err != nil {
			return nil, err
		}
		d.pinned = pin == 1
		if m := partTitle.FindStringSubmatch(d.Title); m != nil {
			i, _ := strconv.Atoi(m[2])
			n, _ := strconv.Atoi(m[3])
			if n > 1 {
				base := strings.TrimSuffix(d.topic, fmt.Sprintf("/part-%d", i))
				key := base + "\x00" + m[1] + "\x00" + m[3]
				if doc, ok := byKey[key]; ok {
					if i < doc.part {
						doc.part, doc.ID = i, d.ID
					}
					doc.pinned = doc.pinned || d.pinned
					continue
				}
				d.Title, d.topic, d.Parts, d.part = m[1], base, n, i
				byKey[key] = d
			}
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// ContextSearch returns the top DigestHitsK search hits for a task, titles
// only, the way Search ranks them, to append to a project's digest.
func (s *Service) ContextSearch(ctx context.Context, project, query string) (SearchResult, error) {
	return s.Search(ctx, SearchInput{Query: query, Project: project, K: DigestHitsK, Detail: DetailTitle})
}

// Stats counts stored memories for diagnostics.
type Stats struct {
	Total, Active, Superseded, Archived int
	WithoutEmbedding                    int // memories with no vector yet
}

// Stats returns the counts shown by the CLI's stats command.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.SQL().QueryRowContext(ctx,
		`select count(*),
		        coalesce(sum(status = 'active'), 0),
		        coalesce(sum(status = 'superseded'), 0),
		        coalesce(sum(status = 'archived'), 0),
		        coalesce(sum(not exists (select 1 from vectors v where v.memory_id = m.id)), 0)
		 from memories m`).
		Scan(&st.Total, &st.Active, &st.Superseded, &st.Archived, &st.WithoutEmbedding)
	return st, err
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
