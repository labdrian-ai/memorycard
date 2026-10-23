package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// chunkTargetBytes is the size importer parts aim for; it sits well below
	// HardCapBytes so a part never lands on the limit.
	chunkTargetBytes = 12 * 1024

	unassignedProject = "unassigned"
)

// ImportRecord is one memory coming from another system. Source and Key
// identify it there, which is what makes an import resumable.
type ImportRecord struct {
	Source, Key string
	Project     string // empty becomes "unassigned"
	Scope       string
	Type        string
	Title, Body string
	TopicKey    string
	Pinned      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ImportLink says that the record with key Replacement replaced the record
// with key Replaced, both in the same Source.
type ImportLink struct {
	Source, Replacement, Replaced string
}

// ImportReport counts what an import did.
type ImportReport struct {
	Imported        int // source records stored as new memories
	AlreadyImported int // skipped because a previous run stored them
	Duplicates      int // merged into an existing memory with identical content
	Empty           int // skipped: nothing left after redaction
	Chunked         int // records split because they exceeded the hard cap
	Parts           int // memories created for those chunked records
	Linked          int // supersessions applied
	LinksSkipped    int // links whose endpoints were not imported
}

// Add accumulates another report into r.
func (r *ImportReport) Add(o ImportReport) {
	r.Imported += o.Imported
	r.AlreadyImported += o.AlreadyImported
	r.Duplicates += o.Duplicates
	r.Empty += o.Empty
	r.Chunked += o.Chunked
	r.Parts += o.Parts
	r.Linked += o.Linked
	r.LinksSkipped += o.LinksSkipped
}

// Import stores records preserving their metadata and timestamps, then
// applies the links, all in one transaction. Records that are too large are
// split into parts instead of truncated. It never calls the embedder; run
// EmbedPending afterwards.
func (s *Service) Import(ctx context.Context, recs []ImportRecord, links []ImportLink) (ImportReport, error) {
	var rep ImportReport
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()

	for _, r := range recs {
		if err := s.importOne(ctx, tx, r, &rep); err != nil {
			return ImportReport{}, fmt.Errorf("import %s/%s: %w", r.Source, r.Key, err)
		}
	}
	for _, l := range links {
		if err := importLink(ctx, tx, l, &rep); err != nil {
			return ImportReport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ImportReport{}, err
	}
	return rep, nil
}

func (s *Service) importOne(ctx context.Context, tx *sql.Tx, r ImportRecord, rep *ImportReport) error {
	var seen int
	err := tx.QueryRowContext(ctx,
		`select 1 from import_map where source = ? and source_key = ? and part = 1`, r.Source, r.Key).Scan(&seen)
	if err == nil {
		rep.AlreadyImported++
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	project := strings.TrimSpace(r.Project)
	if project == "" {
		project = unassignedProject
	}
	title := redact(strings.TrimSpace(r.Title))
	body := redact(strings.TrimSpace(r.Body))
	parts := splitBody(body, chunkTargetBytes)

	created, updated := stampOf(r.CreatedAt, s.now), stampOf(r.UpdatedAt, s.now)
	if r.UpdatedAt.IsZero() {
		updated = created
	}

	for i, part := range parts {
		in := SaveInput{Project: project, Scope: r.Scope, Type: r.Type, Title: title, Body: part, TopicKey: r.TopicKey}
		if len(parts) > 1 {
			in.Title = fmt.Sprintf("%s (part %d/%d)", title, i+1, len(parts))
			if i > 0 && in.TopicKey != "" {
				in.TopicKey = fmt.Sprintf("%s/part-%d", topicKey(in.TopicKey), i+1)
			}
		}
		in, err := normalize(in)
		if errors.Is(err, ErrEmpty) {
			rep.Empty++
			return nil
		}
		if err != nil {
			return err
		}
		id, fresh, err := importRow(ctx, tx, in, r.Pinned, created, updated)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`insert into import_map(source, source_key, part, memory_id) values (?,?,?,?)`,
			r.Source, r.Key, i+1, id); err != nil {
			return err
		}
		if i == 0 {
			if fresh {
				rep.Imported++
			} else {
				rep.Duplicates++
			}
		}
		if fresh && len(parts) > 1 {
			rep.Parts++
		}
	}
	if len(parts) > 1 {
		rep.Chunked++
	}
	return nil
}

// importRow inserts one row, or reuses the existing row with identical
// content in the same project.
func importRow(ctx context.Context, tx *sql.Tx, in SaveInput, pinned bool, created, updated string) (id int64, fresh bool, err error) {
	hash := contentHash(in.Title, in.Body)
	err = tx.QueryRowContext(ctx,
		`select id from memories where project = ? and content_hash = ? order by id limit 1`,
		in.Project, hash).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	var topic any
	if in.TopicKey != "" {
		topic = in.TopicKey
	}
	r, err := tx.ExecContext(ctx,
		`insert into memories(project, scope, type, title, body, topic_key, content_hash, pinned, created_at, updated_at)
		 values (?,?,?,?,?,?,?,?,?,?)`,
		in.Project, in.Scope, in.Type, in.Title, in.Body, topic, hash, b2i(pinned), created, updated)
	if err != nil {
		return 0, false, err
	}
	id, err = r.LastInsertId()
	return id, true, err
}

func importLink(ctx context.Context, tx *sql.Tx, l ImportLink, rep *ImportReport) error {
	lookup := func(key string) (int64, error) {
		var id int64
		err := tx.QueryRowContext(ctx,
			`select memory_id from import_map where source = ? and source_key = ? and part = 1`, l.Source, key).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	newer, err := lookup(l.Replacement)
	if err != nil {
		return err
	}
	older, err := lookup(l.Replaced)
	if err != nil {
		return err
	}
	if newer == 0 || older == 0 || newer == older {
		rep.LinksSkipped++
		return nil
	}
	r, err := tx.ExecContext(ctx,
		`update memories set status = 'superseded', superseded_by = ? where id = ? and status = 'active'`, newer, older)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n > 0 {
		rep.Linked++
	}
	return nil
}

func stampOf(t time.Time, now func() time.Time) string {
	if t.IsZero() {
		t = now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// splitBody returns body whole when it fits the hard cap. Otherwise it packs
// paragraphs into parts of at most target bytes; a paragraph longer than the
// target is cut on rune boundaries.
func splitBody(body string, target int) []string {
	if len(body) <= HardCapBytes {
		return []string{body}
	}
	var parts []string
	cur := ""
	flush := func() {
		if cur != "" {
			parts = append(parts, cur)
			cur = ""
		}
	}
	for _, p := range strings.Split(body, "\n\n") {
		switch {
		case len(p) > target:
			flush()
			parts = append(parts, cutRunes(p, target)...)
		case cur == "":
			cur = p
		case len(cur)+2+len(p) <= target:
			cur += "\n\n" + p
		default:
			flush()
			cur = p
		}
	}
	flush()
	return parts
}

func cutRunes(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}
