package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/llm"
)

type streamEmitError struct {
	err error
}

func (e streamEmitError) Error() string { return e.err.Error() }
func (e streamEmitError) Unwrap() error { return e.err }

var citationPattern = regexp.MustCompile(`\[([Ee])([0-9]+)\]`)

func buildSystemPrompt(mode, roleText string, intents []Intent) (string, string) {
	parts := []string{readPrompt("system.md"), roleText}
	version := "advisor-chat-v1"
	if mode == ModeReview {
		version = "advisor-review-v1"
		parts = append(parts, readPrompt("review.md"))
	} else {
		parts = append(parts, readPrompt("chat.md"))
	}
	if namesContain(intentNames(intents), IntentMethodology) {
		parts = append(parts, readPrompt("methodology.md"))
		if mode != ModeReview && len(intents) == 1 {
			version = "advisor-methodology-v1"
		}
	}
	return version, strings.Join(parts, "\n\n")
}

func buildUserContent(req Request, intents []Intent, evidence []Evidence, failed []string) (string, error) {
	rawEvidence, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	builder.WriteString("用户问题：\n")
	builder.WriteString(req.Question)
	builder.WriteString("\n\n已确认子意图：\n")
	builder.WriteString(strings.Join(intentNames(intents), ", "))
	if len(failed) > 0 {
		builder.WriteString("\n\n查询失败的子意图（不得编造其事实）：\n")
		builder.WriteString(strings.Join(uniqueStrings(failed), ", "))
	}
	builder.WriteString("\n\n可用证据 JSON（只能引用其中的 evidence_id）：\n")
	if len(evidence) == 0 {
		builder.WriteString("[]")
	} else {
		builder.Write(rawEvidence)
	}
	return builder.String(), nil
}

func (s *Service) generate(ctx context.Context, system string, req Request, intents []Intent, evidence []Evidence, failed []string, emit Emit) (string, error) {
	if s.client == nil {
		return "", ErrLLMNotConfigured
	}
	userContent, err := buildUserContent(req, intents, evidence, failed)
	if err != nil {
		return "", ErrGenerationFailed
	}
	messages := make([]llm.Message, 0, len(req.History)+1)
	for _, turn := range req.History {
		messages = append(messages, llm.Message{Role: turn.Role, Content: turn.Content})
	}
	messages = append(messages, llm.Message{Role: "user", Content: userContent})

	var answer strings.Builder
	var emitErr error
	result, err := s.client.Chat(ctx, llm.ChatRequest{
		System:          system,
		Messages:        messages,
		Temperature:     0.2,
		MaxOutputTokens: s.maxOutputTokens,
	}, func(delta string) error {
		if delta == "" {
			return nil
		}
		answer.WriteString(delta)
		if err := emitEvent(emit, StreamEvent{Type: EventDelta, Delta: delta, Text: delta}); err != nil {
			emitErr = err
			return err
		}
		return nil
	})
	if emitErr != nil {
		return "", streamEmitError{err: emitErr}
	}
	if errors.Is(err, llm.ErrMissingAPIKey) {
		return "", ErrLLMNotConfigured
	}
	if err != nil {
		return "", err
	}
	if answer.Len() == 0 {
		answer.WriteString(result.Text)
	}
	return answer.String(), nil
}

func checkCitations(answer string, evidence []Evidence) Citation {
	validIDs := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		validIDs[item.ID] = true
	}
	validSeen := map[string]bool{}
	invalidSeen := map[string]bool{}
	check := Citation{}
	for _, match := range citationPattern.FindAllStringSubmatch(answer, -1) {
		check.Total++
		id := "E" + match[2]
		if validIDs[id] {
			if !validSeen[id] {
				validSeen[id] = true
				check.Valid = append(check.Valid, id)
			}
			continue
		}
		if !invalidSeen[id] {
			invalidSeen[id] = true
			check.Invalid = append(check.Invalid, id)
		}
	}
	check.Zero = check.Total == 0
	if check.Valid == nil {
		check.Valid = []string{}
	}
	if check.Invalid == nil {
		check.Invalid = []string{}
	}
	return check
}

func namesContain(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
