package worker

import (
	"testing"

	"banana-async-gateway/internal/domain"
)

func TestExtractResultSummarySuccess(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"responseId":"resp-1",
		"modelVersion":"gemini-3-pro-image-preview",
		"usageMetadata":{"totalTokenCount":123},
		"candidates":[
			{
				"finishReason":"STOP",
				"content":{
					"parts":[
						{"inlineData":{"mimeType":"image/png","data":"https://example.com/a.png"}},
						{"inlineData":{"mimeType":"image/png","data":"https://example.com/b.png"}},
						{"text":"ignored text"}
					]
				}
			}
		]
	}`)

	summary, err := ExtractResultSummary(domain.RequestProtocolGeminiGenerateContent, body)
	if err != nil {
		t.Fatalf("ExtractResultSummary() error = %v", err)
	}
	if summary == nil || len(summary.ImageURLs) != 2 {
		t.Fatalf("unexpected summary = %#v", summary)
	}
	if summary.FinishReason != "STOP" || summary.ResponseID != "resp-1" || summary.ModelVersion != "gemini-3-pro-image-preview" {
		t.Fatalf("unexpected summary metadata = %#v", summary)
	}
}

func TestExtractResultSummaryUsesTextForSafetyFailure(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"candidates":[
			{
				"finishReason":"IMAGE_SAFETY",
				"content":{"parts":[{"text":"blocked by safety policy"}]}
			}
		]
	}`)

	_, err := ExtractResultSummary(domain.RequestProtocolGeminiGenerateContent, body)
	if err == nil {
		t.Fatalf("expected summary extraction error")
	}
	if err.Code != "upstream_error" || err.Message != "blocked by safety policy" {
		t.Fatalf("unexpected summary error = %#v", err)
	}
}

func TestExtractResultSummaryFailsWithoutImageOrText(t *testing.T) {
	t.Parallel()

	body := []byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[]}}]}`)

	_, err := ExtractResultSummary(domain.RequestProtocolGeminiGenerateContent, body)
	if err == nil {
		t.Fatalf("expected summary extraction error")
	}
	if err.Message != "upstream returned no image" {
		t.Fatalf("unexpected summary error = %#v", err)
	}
}

func TestExtractResultSummaryOpenAIImageSuccess(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"created":1710000000,
		"data":[
			{"url":"https://example.com/openai-a.png"},
			{"url":"https://example.com/openai-b.png"}
		],
		"usage":{"total_tokens":321,"input_tokens":123,"output_tokens":198}
	}`)

	summary, err := ExtractResultSummary(domain.RequestProtocolOpenAIImageGeneration, body)
	if err != nil {
		t.Fatalf("ExtractResultSummary() error = %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary")
	}
	if len(summary.ImageURLs) != 2 {
		t.Fatalf("ImageURLs len = %d, want %d", len(summary.ImageURLs), 2)
	}
	if summary.ImageURLs[0] != "https://example.com/openai-a.png" || summary.ImageURLs[1] != "https://example.com/openai-b.png" {
		t.Fatalf("unexpected ImageURLs = %#v", summary.ImageURLs)
	}
	if summary.OpenAIImageResult == nil {
		t.Fatalf("expected OpenAIImageResult in summary: %#v", summary)
	}
	if summary.OpenAIImageResult.Created != 1710000000 {
		t.Fatalf("created = %d, want %d", summary.OpenAIImageResult.Created, 1710000000)
	}
	if len(summary.OpenAIImageResult.Data) != 2 {
		t.Fatalf("data len = %d, want %d", len(summary.OpenAIImageResult.Data), 2)
	}
	if summary.OpenAIImageResult.Data[0].URL != "https://example.com/openai-a.png" || summary.OpenAIImageResult.Data[1].URL != "https://example.com/openai-b.png" {
		t.Fatalf("unexpected data = %#v", summary.OpenAIImageResult.Data)
	}
	if got := summary.OpenAIImageResult.Usage["total_tokens"]; got != float64(321) {
		t.Fatalf("usage total_tokens = %#v, want %v", got, 321)
	}
}
