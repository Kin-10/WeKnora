// Package bidgen coordinates bounded, durable bid drafting steps in an existing
// conversation. Stored section text, rather than compacted chat history, is the
// source of truth for the final document.
package bidgen

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type Status string

const (
	StatusPlanning        Status = "planning"
	StatusAwaitingOutline Status = "awaiting_outline"
	StatusRunning         Status = "running"
	StatusAwaitingInput   Status = "awaiting_input"
	StatusPaused          Status = "paused"
	StatusFailed          Status = "failed"
	StatusCompleted       Status = "completed"
	StatusCancelled       Status = "cancelled"
)

type Phase string

const (
	PhasePlanning Phase = "planning"
	PhaseSection  Phase = "section"
	PhaseExport   Phase = "export"
)

var (
	ErrNotFound      = errors.New("bid generation task not found")
	ErrActiveTask    = errors.New("a bid generation task already exists in this conversation")
	ErrStaleRevision = errors.New("bid generation task changed; refresh before submitting")
	ErrInvalidAction = errors.New("invalid bid generation action for the current stage")
	ErrInvalidInput  = errors.New("invalid bid generation input")
	ErrLeaseHeld     = errors.New("bid generation step is already running")
)

// Task has one active row per tenant/session. Paused, failed and awaiting tasks
// remain active until completed or cancelled, so a second draft cannot silently
// replace unfinished work. Revision is an optimistic concurrency token.
type Task struct {
	ID             string     `json:"id" gorm:"primaryKey;type:text"`
	TenantID       uint64     `json:"tenant_id" gorm:"not null"`
	SessionID      string     `json:"session_id" gorm:"type:text;not null"`
	UserID         string     `json:"user_id" gorm:"type:text;not null"`
	Status         Status     `json:"status" gorm:"type:text;not null"`
	Revision       int64      `json:"revision" gorm:"not null;default:1"`
	State          State      `json:"state" gorm:"type:jsonb;not null"`
	LastError      string     `json:"last_error,omitempty" gorm:"type:text;not null;default:''"`
	LeaseOwner     string     `json:"-" gorm:"type:text;not null;default:''"`
	LeaseExpiresAt *time.Time `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (Task) TableName() string { return "bid_generation_tasks" }

type State struct {
	RequestSnapshot  json.RawMessage `json:"request_snapshot"`
	Phase            Phase           `json:"phase"`
	Plan             *Plan           `json:"plan,omitempty"`
	Sections         []Section       `json:"sections,omitempty"`
	SectionIndex     int             `json:"section_index"`
	PendingMessageID string          `json:"pending_message_id,omitempty"`
	LastMessageID    string          `json:"last_message_id,omitempty"`
	PendingInput     string          `json:"pending_input,omitempty"`
	UserReplies      []UserReply     `json:"user_replies,omitempty"`
	RetryCount       int             `json:"retry_count"`
	NoProgressCount  int             `json:"no_progress_count"`
	TotalCalls       int             `json:"total_calls"`
	RunStartCalls    int             `json:"run_start_calls"`
	ResumeStatus     Status          `json:"resume_status,omitempty"`
	Export           *Artifact       `json:"export,omitempty"`
}

func (s State) Value() (driver.Value, error) { return json.Marshal(s) }

func (s *State) Scan(value interface{}) error {
	var data []byte
	switch value := value.(type) {
	case []byte:
		data = value
	case string:
		data = []byte(value)
	case nil:
		*s = State{}
		return nil
	default:
		return errors.New("invalid bid generation state")
	}
	return json.Unmarshal(data, s)
}

type Plan struct {
	Title       string        `json:"title"`
	Sections    []SectionSpec `json:"sections"`
	Facts       string        `json:"facts"`
	FormatNotes string        `json:"format_notes"`
	SourceNotes string        `json:"source_notes"`
}

type SectionSpec struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Requirements []string `json:"requirements"`
	TargetWords  int      `json:"target_words"`
}

type Section struct {
	SectionSpec
	Content       string           `json:"content"`
	Completed     bool             `json:"completed"`
	Truncated     bool             `json:"truncated"`
	Version       int              `json:"version"`
	Versions      []SectionVersion `json:"versions,omitempty"`
	Attempts      int              `json:"attempts"`
	LastMessageID string           `json:"last_message_id,omitempty"`
}

type SectionVersion struct {
	Version   int       `json:"version"`
	Content   string    `json:"content"`
	MessageID string    `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type UserReply struct {
	Text      string    `json:"text"`
	MessageID string    `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Artifact struct {
	MessageID string `json:"message_id"`
	Index     int    `json:"index"`
	FileName  string `json:"file_name"`
	Handle    string `json:"handle,omitempty"`
}

type ExportResult = Artifact

type StartRequest struct {
	TenantID        uint64
	SessionID       string
	UserID          string
	RequestSnapshot json.RawMessage
}

type Scope struct {
	TenantID         uint64
	SessionID        string
	UserID           string
	TaskID           string
	ExpectedRevision int64
}

type RespondRequest struct {
	Action    string `json:"action"`
	Text      string `json:"text,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// Filled only by the trusted adapter after validating newly uploaded files.
	RequestSnapshot json.RawMessage `json:"-"`
}

type GenerationRequest struct {
	Phase        Phase
	Prompt       string
	SectionID    string
	Continuation bool
}

type Output struct {
	Content   string
	Truncated bool
	MessageID string
}

type Runner interface {
	Generate(context.Context, *Task, GenerationRequest) (Output, error)
	Export(context.Context, *Task) (*Artifact, error)
}

type EnqueueFunc func(context.Context, string) error
