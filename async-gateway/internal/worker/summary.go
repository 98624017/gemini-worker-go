package worker

import (
	"encoding/json"
	"strings"

	"banana-async-gateway/internal/domain"
)

const noImageMessage = "upstream returned no image"

type SummaryError struct {
	Code    string
	Message string
}

func (e *SummaryError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type responseEnvelope struct {
	ResponseID    string              `json:"responseId"`
	ModelVersion  string              `json:"modelVersion"`
	UsageMetadata map[string]any      `json:"usageMetadata"`
	Candidates    []responseCandidate `json:"candidates"`
}

type responseCandidate struct {
	FinishReason string          `json:"finishReason"`
	Content      responseContent `json:"content"`
}

type responseContent struct {
	Parts []responsePart `json:"parts"`
}

type responsePart struct {
	Text       string             `json:"text"`
	InlineData responseInlineData `json:"inlineData"`
}

type responseInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func ExtractResultSummary(protocol domain.RequestProtocol, body []byte) (*domain.ResultSummary, *SummaryError) {
	switch protocol {
	case domain.RequestProtocolOpenAIImageGeneration:
		return extractOpenAIImageResultSummary(body)
	default:
		return extractGeminiResultSummary(body)
	}
}

func extractGeminiResultSummary(body []byte) (*domain.ResultSummary, *SummaryError) {
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, &SummaryError{
			Code:    "upstream_error",
			Message: err.Error(),
		}
	}

	summary := &domain.ResultSummary{
		ResponseID:    strings.TrimSpace(envelope.ResponseID),
		ModelVersion:  strings.TrimSpace(envelope.ModelVersion),
		UsageMetadata: envelope.UsageMetadata,
	}

	var textParts []string
	for _, candidate := range envelope.Candidates {
		if summary.FinishReason == "" {
			summary.FinishReason = strings.TrimSpace(candidate.FinishReason)
		}
		for _, part := range candidate.Content.Parts {
			if data := strings.TrimSpace(part.InlineData.Data); data != "" {
				summary.ImageURLs = append(summary.ImageURLs, data)
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				textParts = append(textParts, text)
			}
		}
	}

	if len(textParts) > 0 {
		summary.TextSummary = strings.Join(textParts, "\n")
	}

	if len(summary.ImageURLs) > 0 {
		return summary, nil
	}

	if summary.TextSummary != "" {
		return nil, &SummaryError{
			Code:    "upstream_error",
			Message: summary.TextSummary,
		}
	}

	return nil, &SummaryError{
		Code:    "upstream_error",
		Message: noImageMessage,
	}
}

type openAIImageEnvelope struct {
	Created int64                    `json:"created"`
	Data    []domain.OpenAIImageData `json:"data"`
	Usage   map[string]any           `json:"usage"`
}

func extractOpenAIImageResultSummary(body []byte) (*domain.ResultSummary, *SummaryError) {
	var envelope openAIImageEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, &SummaryError{
			Code:    "upstream_error",
			Message: err.Error(),
		}
	}

	summary := &domain.ResultSummary{
		OpenAIImageResult: &domain.OpenAIImageResult{
			Created: envelope.Created,
			Data:    make([]domain.OpenAIImageData, 0, len(envelope.Data)),
			Usage:   envelope.Usage,
		},
	}

	for _, item := range envelope.Data {
		url := strings.TrimSpace(item.URL)
		if url == "" {
			continue
		}
		summary.ImageURLs = append(summary.ImageURLs, url)
		summary.OpenAIImageResult.Data = append(summary.OpenAIImageResult.Data, domain.OpenAIImageData{
			URL: url,
		})
	}

	if len(summary.ImageURLs) > 0 {
		return summary, nil
	}

	return nil, &SummaryError{
		Code:    "upstream_error",
		Message: noImageMessage,
	}
}
