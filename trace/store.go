package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	if recoverySummary := summarizeClaudeRecovery(interaction, events); recoverySummary != "" {
		builder.WriteString("recovery_summary: " + recoverySummary + "\n")
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

var (
	missingTeamPattern  = regexp.MustCompile(`Team "([^"]+)" does not exist\. Call spawnTeam first to create the team\.`)
	staleTeamPattern    = regexp.MustCompile(`Already leading team "([^"]+)"\.`)
	simplifyReviewNames = []string{"reuse-reviewer", "quality-reviewer", "efficiency-reviewer"}
)

func summarizeClaudeRecovery(interaction Interaction, events []InteractionEvent) string {
	if interaction.ClientAPI != "claude" || interaction.Path != "/v1/messages" {
		return ""
	}

	parts := make([]string, 0, 3)
	seen := make(map[string]struct{})
	reviewerCounts := make(map[string]int, len(simplifyReviewNames))

	for _, event := range events {
		body := normalizeTraceBodyForRecoverySummary(event.Body)
		if body == "" {
			continue
		}
		for _, match := range missingTeamPattern.FindAllStringSubmatch(body, -1) {
			if len(match) < 2 {
				continue
			}
			part := "missing-team:" + strings.TrimSpace(match[1])
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			parts = append(parts, part)
		}
		for _, match := range staleTeamPattern.FindAllStringSubmatch(body, -1) {
			if len(match) < 2 {
				continue
			}
			part := "stale-team:" + strings.TrimSpace(match[1])
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			parts = append(parts, part)
		}
		addSimplifyReviewerProtocolCounts(reviewerCounts, body)
	}

	if allSimplifyReviewersRetried(reviewerCounts) {
		const part = "duplicate-simplify-reviewer-retry"
		if _, ok := seen[part]; !ok {
			parts = append(parts, part)
		}
	}

	return strings.Join(parts, ", ")
}

func normalizeTraceBodyForRecoverySummary(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	body = strings.ReplaceAll(body, `\"`, `"`)
	body = strings.ReplaceAll(body, `\n`, "\n")
	return body
}

func allSimplifyReviewersRetried(counts map[string]int) bool {
	if len(counts) == 0 {
		return false
	}
	for _, reviewer := range simplifyReviewNames {
		if counts[reviewer] < 2 {
			return false
		}
	}
	return true
}

func addSimplifyReviewerProtocolCounts(counts map[string]int, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}

	var payload struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return
	}

	for _, msg := range payload.Messages {
		trimmed := strings.TrimSpace(string(msg.Content))
		if trimmed == "" || trimmed == "null" || trimmed[0] != '[' {
			continue
		}

		var blocks []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}

		for _, block := range blocks {
			if strings.TrimSpace(block.Type) != "tool_use" || strings.TrimSpace(block.Name) != "Agent" {
				continue
			}
			name, _ := block.Input["name"].(string)
			if name = normalizeTraceSimplifyReviewerName(name); name == "" {
				continue
			}
			counts[name]++
		}
	}
}

func normalizeTraceSimplifyReviewerName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "reuse-reviewer", "quality-reviewer", "efficiency-reviewer":
		return name
	default:
		return ""
	}
}
