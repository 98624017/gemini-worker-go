package worker

import (
	"testing"
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

	summary, err := ExtractResultSummary(body)
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

	_, err := ExtractResultSummary(body)
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

	_, err := ExtractResultSummary(body)
	if err == nil {
		t.Fatalf("expected summary extraction error")
	}
	if err.Message != "upstream returned no image" {
		t.Fatalf("unexpected summary error = %#v", err)
	}
}
