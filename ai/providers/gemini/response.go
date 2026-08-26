package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

const maxGeminiResponseBytes = 16 << 20

type decodedResponse struct {
	Response     *core.AIResponse
	UsageDetails *core.AIUsageDetails
}

type decodedStreamEvent struct {
	Text         string
	FinishReason string
	Usage        *normalizedUsage
	Done         bool
}

type profileStreamDecoder interface {
	Next(context.Context) (decodedStreamEvent, error)
}

type normalizedUsage struct {
	Input       int
	Output      int
	Total       int
	CachedInput int64
	Reasoning   int64
}

func normalizedUsageFromGemini(usage UsageMetadata) normalizedUsage {
	return normalizedUsage{
		Input:       usage.PromptTokenCount,
		Output:      usage.CandidatesTokenCount,
		Total:       usage.TotalTokenCount,
		CachedInput: int64(usage.CachedContentTokenCount),
		Reasoning:   int64(usage.ThoughtsTokenCount),
	}
}

func (usage normalizedUsage) coreUsage() (core.TokenUsage, *core.AIUsageDetails) {
	return core.TokenUsage{
		PromptTokens:     usage.Input,
		CompletionTokens: usage.Output,
		TotalTokens:      usage.Total,
	}, &core.AIUsageDetails{
		CachedInputTokens: usage.CachedInput,
		ReasoningTokens:   usage.Reasoning,
	}
}

func (profile wireProfile) decodeBuffered(body io.Reader, requestedModel string) (*decodedResponse, error) {
	if profile != selectedWireProfile {
		return nil, errors.New("unsupported Gemini wire profile")
	}
	limited := io.LimitReader(body, maxGeminiResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Gemini response: %w", err)
	}
	if len(data) > maxGeminiResponseBytes {
		return nil, errors.New("gemini response exceeds the supported size")
	}
	var response GeminiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Gemini response: %w", err)
	}
	if len(response.Candidates) == 0 {
		return nil, errors.New("no candidates in Gemini response")
	}
	var content strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		if !part.Thought {
			content.WriteString(part.Text)
		}
	}
	if content.Len() == 0 {
		return nil, errors.New("no text content in Gemini response")
	}
	model := response.ModelVersion
	if model == "" {
		model = requestedModel
	}
	usage := normalizedUsageFromGemini(response.UsageMetadata)
	tokens, details := usage.coreUsage()
	return &decodedResponse{
		Response: &core.AIResponse{
			Content:  content.String(),
			Model:    model,
			Provider: "gemini",
			Usage:    tokens,
		},
		UsageDetails: details,
	}, nil
}

type generateContentStreamDecoder struct {
	scanner *bufio.Scanner
}

func (profile wireProfile) newStreamDecoder(body io.Reader) (profileStreamDecoder, error) {
	if profile != selectedWireProfile {
		return nil, errors.New("unsupported Gemini wire profile")
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxGeminiResponseBytes)
	return &generateContentStreamDecoder{scanner: scanner}, nil
}

func (decoder *generateContentStreamDecoder) Next(ctx context.Context) (decodedStreamEvent, error) {
	for {
		select {
		case <-ctx.Done():
			return decodedStreamEvent{}, ctx.Err()
		default:
		}

		if !decoder.scanner.Scan() {
			if err := decoder.scanner.Err(); err != nil {
				return decodedStreamEvent{}, fmt.Errorf("read Gemini stream: %w", err)
			}
			return decodedStreamEvent{}, io.EOF
		}
		line := strings.TrimSpace(decoder.scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return decodedStreamEvent{Done: true}, nil
		}
		if len(data) > maxGeminiResponseBytes {
			return decodedStreamEvent{}, errors.New("gemini stream event exceeds the supported size")
		}
		var chunk StreamChunk
		if decodeErr := json.Unmarshal([]byte(data), &chunk); decodeErr != nil {
			return decodedStreamEvent{}, fmt.Errorf("decode Gemini stream event: %w", decodeErr)
		}
		event := decodedStreamEvent{}
		if chunk.UsageMetadata != nil {
			usage := normalizedUsageFromGemini(*chunk.UsageMetadata)
			event.Usage = &usage
		}
		if len(chunk.Candidates) > 0 {
			candidate := chunk.Candidates[0]
			for _, part := range candidate.Content.Parts {
				if !part.Thought {
					event.Text += part.Text
				}
			}
			event.FinishReason = candidate.FinishReason
			event.Done = candidate.FinishReason != ""
		}
		return event, nil
	}
}
