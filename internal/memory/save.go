// Package memory implements the episodic memory operations on top of store.
package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/store"
)

const (
	// SoftTargetBytes is the body size above which Save warns.
	SoftTargetBytes = 4 * 1024
	// HardCapBytes is the largest body stored in one memory. Save splits bigger
	// ones into parts; Update rejects them. Nothing is ever truncated.
	HardCapBytes = 16 * 1024
	// MaxVersions is how many prior versions of a topic_key memory are kept.
	MaxVersions = 5

	maxTopicKeyBytes = 120
	redactionMarker  = "[REDACTED]"
)

// Actions reported by Save.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged"
	ActionDuplicate = "duplicate"
)

var (
	ErrEmpty           = errors.New("title and body must not be empty")
	ErrNoProject       = errors.New("project is required")
	ErrBadScope        = errors.New("scope must be project, personal or global")
	ErrTooLarge        = errors.New("body exceeds the hard size cap")
	ErrSupersedeTarget = errors.New("supersedes must name an existing memory of the same project")
)

var privateTag = regexp.MustCompile(`(?s)<private>.*?</private>`)

// Service runs memory operations against a store.
type Service struct {
	db       *store.DB
	now      func() time.Time
	embedder embed.Embedder // optional; nil means text-only
	hybrid   bool           // fuse dense and lexical rankings when searching
}

// New returns a Service over db.
func New(db *store.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// SaveInput describes a memory to save.
type SaveInput struct {
	Project    string
	Scope      string // project (default), personal or global
	Type       string // defaults to "manual"
	Title      string
	Body       string
	TopicKey   string
	Supersedes int64 // id of a memory this one replaces
}

// SaveResult reports what Save did.
type SaveResult struct {
	ID       int64
	Action   string
	Warnings []string
	Hint     *Hint   // set when a similar memory already exists; never blocks the write
	Parts    []int64 // ids of every part, in order, when the body was split
}

// Save stores a memory following the write rules: redaction, size caps,
// exact-duplicate suppression, topic_key upsert with version snapshots and
// optional supersession. A body over HardCapBytes is stored as parts, the way
// the importer splits records; the result describes part 1.
func (s *Service) Save(ctx context.Context, in SaveInput) (SaveResult, error) {
	in, err := normalizeFields(in)
	if err != nil {
		return SaveResult{}, err
	}
	pieces := splitInput(in, contentParts(in.Body))
	var res SaveResult
	switch {
	case len(pieces) > 1:
		res.Warnings = append(res.Warnings, fmt.Sprintf("body is %d bytes; saved as %d parts of at most %d bytes",
			len(in.Body), len(pieces), chunkTargetBytes))
	case len(in.Body) > SoftTargetBytes:
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("body is %d bytes; consider splitting it (target <= %d)", len(in.Body), SoftTargetBytes))
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback()

	if in.Supersedes != 0 {
		var project string
		err := tx.QueryRowContext(ctx, `select project from memories where id = ?`, in.Supersedes).Scan(&project)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && project != in.Project) {
			return SaveResult{}, ErrSupersedeTarget
		}
		if err != nil {
			return SaveResult{}, err
		}
	}

	oldParts := 0
	if in.TopicKey != "" {
		if oldParts, err = savedPartCount(ctx, tx, in); err != nil {
			return SaveResult{}, err
		}
	}
	ids := make([]int64, len(pieces))
	actions := make([]string, len(pieces))
	for i, p := range pieces {
		if ids[i], actions[i], err = s.write(ctx, tx, p, contentHash(p.Title, p.Body), stamp); err != nil {
			return SaveResult{}, err
		}
		if in.Supersedes == ids[i] {
			return SaveResult{}, ErrSupersedeTarget
		}
		// A part archived when the document shrank is in use again. The key
		// check keeps a duplicate match elsewhere from being revived.
		if i > 0 && p.TopicKey != "" {
			if _, err := tx.ExecContext(ctx,
				`update memories set status='active', updated_at=? where id=? and topic_key=? and status='archived'`,
				stamp, ids[i], p.TopicKey); err != nil {
				return SaveResult{}, err
			}
		}
	}
	// Parts the previous version had beyond the new count are left over.
	for i := len(pieces) + 1; i <= oldParts; i++ {
		if _, err := tx.ExecContext(ctx,
			`update memories set status='archived', updated_at=?
			 where project=? and scope=? and topic_key=? and status='active'`,
			stamp, in.Project, in.Scope, partKey(in.TopicKey, i)); err != nil {
			return SaveResult{}, err
		}
	}
	if in.Supersedes != 0 {
		if _, err := tx.ExecContext(ctx,
			`update memories set status='superseded', superseded_by=?, updated_at=? where id=?`,
			ids[0], stamp, in.Supersedes); err != nil {
			return SaveResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	res.ID, res.Action = ids[0], overallAction(actions)
	if len(pieces) > 1 {
		res.Parts = ids
	}
	for i, p := range pieces {
		if i == 0 {
			s.afterWrite(ctx, ids[i], p.Project, p.Title, p.Body, actions[i], &res)
			continue
		}
		// Only part 1 may report a similar memory; later parts would find part 1.
		var r SaveResult
		s.afterWrite(ctx, ids[i], p.Project, p.Title, p.Body, actions[i], &r)
		res.Warnings = append(res.Warnings, r.Warnings...)
	}
	return res, nil
}

// splitInput returns one input per body part. With several parts, each title
// gets "(part i/N)" and parts after the first get the topic key <key>/part-i.
func splitInput(in SaveInput, bodies []string) []SaveInput {
	if len(bodies) == 1 {
		return []SaveInput{in}
	}
	out := make([]SaveInput, len(bodies))
	for i, b := range bodies {
		p := in
		p.Title = fmt.Sprintf("%s (part %d/%d)", in.Title, i+1, len(bodies))
		p.Body = b
		if i > 0 && in.TopicKey != "" {
			p.TopicKey = partKey(in.TopicKey, i+1)
		}
		out[i] = p
	}
	return out
}

func partKey(key string, i int) string { return fmt.Sprintf("%s/part-%d", key, i) }

var partOneTitle = regexp.MustCompile(`\(part 1/(\d+)\)$`)

// savedPartCount returns how many parts the live memory holding in's topic key
// was split into: N when its title ends in "(part 1/N)", otherwise 1 (0 when
// there is none). Only those parts can be left over, so keys that merely look
// like parts are never touched.
func savedPartCount(ctx context.Context, tx *sql.Tx, in SaveInput) (int, error) {
	var title string
	err := tx.QueryRowContext(ctx,
		`select title from memories where project=? and scope=? and topic_key=? and status != 'superseded'`,
		in.Project, in.Scope, in.TopicKey).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	m := partOneTitle.FindStringSubmatch(title)
	if m == nil {
		return 1, nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 1, nil
	}
	return n, nil
}

// overallAction sums up the parts' actions: unchanged or duplicate only when
// every part was, created when part 1 is new, updated otherwise.
func overallAction(actions []string) string {
	for _, a := range actions[1:] {
		if a != actions[0] {
			if actions[0] == ActionCreated {
				return ActionCreated
			}
			return ActionUpdated
		}
	}
	return actions[0]
}

func (s *Service) write(ctx context.Context, tx *sql.Tx, in SaveInput, hash, stamp string) (int64, string, error) {
	if in.TopicKey != "" {
		var id int64
		var oldHash, oldTitle, oldBody string
		err := tx.QueryRowContext(ctx,
			`select id, content_hash, title, body from memories
			 where project=? and scope=? and topic_key=? and status != 'superseded'
			 order by id desc limit 1`,
			in.Project, in.Scope, in.TopicKey).Scan(&id, &oldHash, &oldTitle, &oldBody)
		switch {
		case err == nil && oldHash == hash:
			return id, ActionUnchanged, nil
		case err == nil:
			return id, ActionUpdated, s.update(ctx, tx, id, oldTitle, oldBody, in, hash, stamp)
		case !errors.Is(err, sql.ErrNoRows):
			return 0, "", err
		}
	}

	var dup int64
	err := tx.QueryRowContext(ctx,
		`select id from memories where project=? and content_hash=? order by id limit 1`,
		in.Project, hash).Scan(&dup)
	if err == nil {
		return dup, ActionDuplicate, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}

	var topic any
	if in.TopicKey != "" {
		topic = in.TopicKey
	}
	r, err := tx.ExecContext(ctx,
		`insert into memories(project, scope, type, title, body, topic_key, content_hash, created_at, updated_at)
		 values (?,?,?,?,?,?,?,?,?)`,
		in.Project, in.Scope, in.Type, in.Title, in.Body, topic, hash, stamp, stamp)
	if err != nil {
		return 0, "", err
	}
	id, err := r.LastInsertId()
	return id, ActionCreated, err
}

func (s *Service) update(ctx context.Context, tx *sql.Tx, id int64, oldTitle, oldBody string, in SaveInput, hash, stamp string) error {
	var next int
	if err := tx.QueryRowContext(ctx,
		`select coalesce(max(version), 0) + 1 from memory_versions where memory_id=?`, id).Scan(&next); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`insert into memory_versions(memory_id, version, title, body, saved_at) values (?,?,?,?,?)`,
		id, next, oldTitle, oldBody, stamp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`delete from memory_versions where memory_id=? and version <= ?`, id, next-MaxVersions); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`update memories set type=?, title=?, body=?, content_hash=?, updated_at=? where id=?`,
		in.Type, in.Title, in.Body, hash, stamp, id)
	return err
}

// normalize validates and cleans in for a single memory, size cap included.
func normalize(in SaveInput) (SaveInput, error) {
	in, err := normalizeFields(in)
	if err != nil {
		return in, err
	}
	if len(in.Body) > HardCapBytes {
		return in, fmt.Errorf("%w: body is %d bytes, cap is %d; split it into smaller memories", ErrTooLarge, len(in.Body), HardCapBytes)
	}
	return in, nil
}

// normalizeFields is normalize without the size cap.
func normalizeFields(in SaveInput) (SaveInput, error) {
	in.Project = strings.TrimSpace(in.Project)
	if in.Project == "" {
		return in, ErrNoProject
	}
	switch in.Scope = strings.TrimSpace(in.Scope); in.Scope {
	case "":
		in.Scope = "project"
	case "project", "personal", "global":
	default:
		return in, ErrBadScope
	}
	if in.Type = strings.TrimSpace(in.Type); in.Type == "" {
		in.Type = "manual"
	}
	in.Title = redact(strings.TrimSpace(in.Title))
	in.Body = redact(strings.TrimSpace(in.Body))
	if strings.TrimSpace(strings.ReplaceAll(in.Title, redactionMarker, "")) == "" ||
		strings.TrimSpace(strings.ReplaceAll(in.Body, redactionMarker, "")) == "" {
		return in, ErrEmpty
	}
	in.TopicKey = topicKey(in.TopicKey)
	return in, nil
}

func redact(s string) string { return privateTag.ReplaceAllString(s, redactionMarker) }

func topicKey(k string) string {
	k = strings.ToLower(strings.Join(strings.Fields(k), "-"))
	for len(k) > maxTopicKeyBytes {
		_, size := utf8.DecodeLastRuneInString(k)
		k = k[:len(k)-size]
	}
	return k
}

func contentHash(title, body string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(title+"\n"+body), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
