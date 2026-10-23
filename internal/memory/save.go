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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/store"
)

const (
	// SoftTargetBytes is the body size above which Save warns.
	SoftTargetBytes = 4 * 1024
	// HardCapBytes is the largest accepted body; bigger ones are rejected, never truncated.
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
	db  *store.DB
	now func() time.Time
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
}

// Save stores a memory following the write rules: redaction, size caps,
// exact-duplicate suppression, topic_key upsert with version snapshots and
// optional supersession.
func (s *Service) Save(ctx context.Context, in SaveInput) (SaveResult, error) {
	in, err := normalize(in)
	if err != nil {
		return SaveResult{}, err
	}
	var res SaveResult
	if len(in.Body) > SoftTargetBytes {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("body is %d bytes; consider splitting it (target <= %d)", len(in.Body), SoftTargetBytes))
	}
	hash := contentHash(in.Title, in.Body)
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

	id, action, err := s.write(ctx, tx, in, hash, stamp)
	if err != nil {
		return SaveResult{}, err
	}
	if in.Supersedes != 0 {
		if in.Supersedes == id {
			return SaveResult{}, ErrSupersedeTarget
		}
		if _, err := tx.ExecContext(ctx,
			`update memories set status='superseded', superseded_by=?, updated_at=? where id=?`,
			id, stamp, in.Supersedes); err != nil {
			return SaveResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	res.ID, res.Action = id, action
	return res, nil
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

func normalize(in SaveInput) (SaveInput, error) {
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
	if len(in.Body) > HardCapBytes {
		return in, fmt.Errorf("%w: body is %d bytes, cap is %d; split it into smaller memories", ErrTooLarge, len(in.Body), HardCapBytes)
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
