package trace

import "time"

const InteractionIDHeader = "X-GPTB2O-Interaction-ID"

type EventKind string

const (
	EventClientRequest   EventKind = "client_request"
	EventBackendRequest  EventKind = "backend_request"
	EventBackendResponse EventKind = "backend_response"
	EventClientResponse  EventKind = "client_response"
)

type Interaction struct {
	ID            uint   `gorm:"primaryKey"`
	InteractionID string `gorm:"uniqueIndex;size:128;not null"`
	Method        string `gorm:"size:16"`
	Path          string `gorm:"size:512"`
	Query         string `gorm:"size:1024"`
	ClientAPI     string `gorm:"size:32"`
	Model         string `gorm:"size:128"`
	Stream        bool
	StatusCode    int
	ErrorSummary  string    `gorm:"type:text"`
	StartedAt     time.Time `gorm:"not null"`
	FinishedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type InteractionEvent struct {
	ID            uint      `gorm:"primaryKey"`
	InteractionID string    `gorm:"index;size:128;not null"`
	Seq           int       `gorm:"index;not null"`
	Kind          EventKind `gorm:"size:32;not null"`
	Method        string    `gorm:"size:16"`
	Path          string    `gorm:"size:512"`
	URL           string    `gorm:"type:text"`
	StatusCode    int
	ContentType   string `gorm:"size:256"`
	HeadersJSON   string `gorm:"type:text"`
	Body          string `gorm:"type:text"`
	BodyTruncated bool
	Summary       string `gorm:"type:text"`
	DurationMs    int64
	CreatedAt     time.Time
}
