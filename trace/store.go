package trace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type Store struct {
	db *gorm.DB
	mu sync.Mutex
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("trace db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create trace db dir: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open trace db: %w", err)
	}
	if err := db.AutoMigrate(&Interaction{}, &InteractionEvent{}); err != nil {
		return nil, fmt.Errorf("migrate trace db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) StartInteraction(interaction Interaction) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("trace store is nil")
	}
	if strings.TrimSpace(interaction.InteractionID) == "" {
		return fmt.Errorf("interaction_id is required")
	}
	if interaction.StartedAt.IsZero() {
		interaction.StartedAt = time.Now()
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "interaction_id"}},
		DoNothing: true,
	}).Create(&interaction).Error
}

func (s *Store) AppendEvent(event InteractionEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("trace store is nil")
	}
	if strings.TrimSpace(event.InteractionID) == "" {
		return fmt.Errorf("interaction_id is required")
	}
	if event.Kind == "" {
		return fmt.Errorf("event kind is required")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if event.Seq <= 0 {
		s.mu.Lock()
		defer s.mu.Unlock()

		var last InteractionEvent
		err := s.db.Where("interaction_id = ?", event.InteractionID).
			Order("seq DESC, id DESC").
			Limit(1).
			Take(&last).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			event.Seq = 1
		case err != nil:
			return err
		default:
			event.Seq = last.Seq + 1
		}
	}
	return s.db.Create(&event).Error
}

func (s *Store) FinishInteraction(interactionID string, statusCode int, errorSummary string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("trace store is nil")
	}
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("interaction_id is required")
	}
	finishedAt := time.Now()
	return s.db.Model(&Interaction{}).
		Where("interaction_id = ?", interactionID).
		Updates(map[string]any{
			"status_code":   statusCode,
			"error_summary": errorSummary,
			"finished_at":   &finishedAt,
		}).Error
}

func (s *Store) GetInteraction(interactionID string) (Interaction, []InteractionEvent, error) {
	if s == nil || s.db == nil {
		return Interaction{}, nil, fmt.Errorf("trace store is nil")
	}
	var interaction Interaction
	if err := s.db.Where("interaction_id = ?", interactionID).Take(&interaction).Error; err != nil {
		return Interaction{}, nil, err
	}
	var events []InteractionEvent
	if err := s.db.Where("interaction_id = ?", interactionID).
		Order("seq ASC, id ASC").
		Find(&events).Error; err != nil {
		return Interaction{}, nil, err
	}
	return interaction, events, nil
}

func FormatInteractionReport(interaction Interaction, events []InteractionEvent) string {
	var builder strings.Builder
	builder.WriteString("interaction_id: " + interaction.InteractionID + "\n")
	builder.WriteString("method: " + interaction.Method + "\n")
	builder.WriteString("path: " + interaction.Path + "\n")
	builder.WriteString("client_api: " + interaction.ClientAPI + "\n")
	builder.WriteString("model: " + interaction.Model + "\n")
	builder.WriteString(fmt.Sprintf("stream: %t\n", interaction.Stream))
	builder.WriteString(fmt.Sprintf("status_code: %d\n", interaction.StatusCode))
	if interaction.ErrorSummary != "" {
		builder.WriteString("error_summary: " + interaction.ErrorSummary + "\n")
	}
	if !interaction.StartedAt.IsZero() {
		builder.WriteString("started_at: " + interaction.StartedAt.Format(time.RFC3339Nano) + "\n")
	}
	if interaction.FinishedAt != nil {
		builder.WriteString("finished_at: " + interaction.FinishedAt.Format(time.RFC3339Nano) + "\n")
	}
	for _, event := range events {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("[%d] %s\n", event.Seq, event.Kind))
		if event.Method != "" {
			builder.WriteString("method: " + event.Method + "\n")
		}
		if event.Path != "" {
			builder.WriteString("path: " + event.Path + "\n")
		}
		if event.URL != "" {
			builder.WriteString("url: " + event.URL + "\n")
		}
		if event.StatusCode != 0 {
			builder.WriteString(fmt.Sprintf("status_code: %d\n", event.StatusCode))
		}
		if event.ContentType != "" {
			builder.WriteString("content_type: " + event.ContentType + "\n")
		}
		if event.Summary != "" {
			builder.WriteString("summary: " + event.Summary + "\n")
		}
		if event.HeadersJSON != "" {
			builder.WriteString("headers: " + event.HeadersJSON + "\n")
		}
		if event.Body != "" {
			builder.WriteString("body:\n" + event.Body + "\n")
		}
		if event.BodyTruncated {
			builder.WriteString("body_truncated: true\n")
		}
	}
	return builder.String()
}
