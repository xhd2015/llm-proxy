package anthropic2openai

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Ported from cc-switch proxy/tool_media.rs tests (the chat-plan tests are
// omitted: plan_chat_tool_output_media belongs to the chat-only conversion
// that is out of scope for this package).

func largeImageDataURL() string {
	return "data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAAAE", 400)
}

func mustChatMediaPart(t *testing.T, part map[string]any, scope toolMediaScope) map[string]any {
	t.Helper()
	mapped, ok := chatMediaPartFromToolPart(part, scope)
	if !ok {
		t.Fatalf("expected media part for %s", canonicalJSONString(part))
	}
	return mapped
}

func TestMapsInputImageAndMergesTopLevelDetail(t *testing.T) {
	part := map[string]any{
		"type":      "input_image",
		"image_url": "https://example.com/image.png",
		"detail":    "high",
	}
	mapped := mustChatMediaPart(t, part, mediaScopeAllSupported)
	if mapped["type"] != "image_url" {
		t.Fatalf("type = %v", mapped["type"])
	}
	imageURL := mapped["image_url"].(map[string]any)
	if imageURL["url"] != "https://example.com/image.png" || imageURL["detail"] != "high" {
		t.Fatalf("image_url = %+v", imageURL)
	}
}

func TestMapsAlreadyChatShapedImageURL(t *testing.T) {
	part := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url":    "https://example.com/image.png",
			"detail": "low",
		},
		"cache_control":           map[string]any{"type": "ephemeral"},
		"prompt_cache_breakpoint": true,
	}
	mapped := mustChatMediaPart(t, part, mediaScopeAllSupported)
	requireEqual(t, mapped, map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url":    "https://example.com/image.png",
			"detail": "low",
		},
	})
}

func TestMapsAnthropicAndMCPImageShapes(t *testing.T) {
	anthropic := map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "base64", "media_type": "image/jpeg", "data": "YWJj",
		},
	}
	mcp := map[string]any{
		"type": "image", "mimeType": "image/webp", "data": "ZGVm",
	}
	anthropicURL := map[string]any{
		"type":   "image",
		"source": map[string]any{"url": "https://example.com/anthropic.png"},
	}

	anthropicPart := mustChatMediaPart(t, anthropic, mediaScopeAllSupported)
	mcpPart := mustChatMediaPart(t, mcp, mediaScopeAllSupported)
	anthropicURLPart := mustChatMediaPart(t, anthropicURL, mediaScopeAllSupported)

	if got := anthropicPart["image_url"].(map[string]any)["url"]; got != "data:image/jpeg;base64,YWJj" {
		t.Fatalf("anthropic url = %v", got)
	}
	if got := mcpPart["image_url"].(map[string]any)["url"]; got != "data:image/webp;base64,ZGVm" {
		t.Fatalf("mcp url = %v", got)
	}
	if got := anthropicURLPart["image_url"].(map[string]any)["url"]; got != "https://example.com/anthropic.png" {
		t.Fatalf("anthropic url part = %v", got)
	}
}

func TestMapsAnthropicSourceDataWhenOptionalURLEmpty(t *testing.T) {
	part := map[string]any{
		"type": "image",
		"source": map[string]any{
			"url": "", "media_type": "image/png", "data": "YWJj",
		},
	}
	mapped := mustChatMediaPart(t, part, mediaScopeAllSupported)
	if got := mapped["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,YWJj" {
		t.Fatalf("url = %v", got)
	}
}

func TestRejectsImageMetadataAndNonImageMCPPayloads(t *testing.T) {
	metadata := map[string]any{"type": "image", "name": "cover"}
	nonImage := map[string]any{
		"type": "image", "mimeType": "text/plain", "data": "aGVsbG8=",
	}
	if _, ok := chatMediaPartFromToolPart(metadata, mediaScopeAllSupported); ok {
		t.Fatal("metadata-only image must be rejected")
	}
	if _, ok := chatMediaPartFromToolPart(nonImage, mediaScopeAllSupported); ok {
		t.Fatal("non-image mcp payload must be rejected")
	}
}

func TestLooseDataImageURLIsMediaButLooseRemoteURLIsNot(t *testing.T) {
	data := any(map[string]any{
		"image_url": map[string]any{"url": "data:application/octet-stream;base64,YWJj"},
	})
	remote := any(map[string]any{
		"image_url": map[string]any{"url": "https://example.com/search-thumbnail.png"},
	})

	if !toolOutputContainsMedia(data, mediaScopeImagesOnly) {
		t.Fatal("loose data URL must be media")
	}
	if toolOutputContainsMedia(remote, mediaScopeImagesOnly) {
		t.Fatal("loose remote URL must not be media")
	}
}

func TestInlineImageScopeRejectsRemoteAndMalformedDataURLs(t *testing.T) {
	inline := any(map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YWJj"}})
	remote := any(map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}})
	missingBase64 := any(map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png,YWJj"}})
	emptyData := any(map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,"}})

	if !toolOutputContainsMedia(inline, mediaScopeInlineImagesOnly) {
		t.Fatal("inline data URL must be media")
	}
	for name, value := range map[string]any{"remote": remote, "missingBase64": missingBase64, "emptyData": emptyData} {
		if toolOutputContainsMedia(value, mediaScopeInlineImagesOnly) {
			t.Fatalf("%s must not be media in inline scope", name)
		}
	}
}

func TestDoesNotScanEmbeddedDataURLsInsidePlainText(t *testing.T) {
	dataURL := largeImageDataURL()
	original := any("<html><img src=\"" + dataURL + "\"></html>")
	value := original
	replacement := map[string]any{"type": "text", "text": "moved"}
	var media []any

	if toolOutputContainsMedia(value, mediaScopeAllSupported) {
		t.Fatal("embedded data URL in plain text must not be media")
	}
	if replaced := stripMediaFromToolValue(&value, &media, mediaScopeAllSupported, replacement, "moved"); replaced != 0 {
		t.Fatalf("replaced = %d", replaced)
	}
	if len(media) != 0 {
		t.Fatalf("media = %v", media)
	}
	if canonicalJSONString(value) != canonicalJSONString(original) {
		t.Fatal("value changed")
	}
}

func TestWholeStringDataURLRespectsThreshold(t *testing.T) {
	large := largeImageDataURL()
	small := "data:image/png;base64,YWJj"
	if _, ok := wholeStringImageDataURL(large); !ok {
		t.Fatal("large data URL must be recognized")
	}
	if _, ok := wholeStringImageDataURL(small); ok {
		t.Fatal("small data URL must stay text")
	}
}

func TestStripsMediaFromJSONStringAndNestedContent(t *testing.T) {
	dataURL := largeImageDataURL()
	encoded := canonicalJSONString(map[string]any{
		"content": []any{
			map[string]any{"type": "input_text", "text": "caption"},
			map[string]any{"type": "input_image", "image_url": dataURL},
		},
	})
	value := any(encoded)
	replacement := map[string]any{"type": "text", "text": "moved"}
	var media []any

	replaced := stripMediaFromToolValue(&value, &media, mediaScopeAllSupported, replacement, "moved")
	if replaced != 1 {
		t.Fatalf("replaced = %d", replaced)
	}
	if len(media) != 1 {
		t.Fatalf("media = %v", media)
	}
	serialized := value.(string)
	if !contains(serialized, `"text":"moved"`) {
		t.Fatalf("serialized = %s", serialized)
	}
	if contains(serialized, "iVBORw0KGgo") {
		t.Fatal("media payload must be stripped")
	}
}

func TestImageOnlyScopeIgnoresFileAndAudio(t *testing.T) {
	file := any(map[string]any{"type": "input_file", "file_id": "file_1"})
	audio := any(map[string]any{
		"type":        "input_audio",
		"input_audio": map[string]any{"data": "YWJj", "format": "wav"},
	})

	if toolOutputContainsMedia(file, mediaScopeImagesOnly) || toolOutputContainsMedia(audio, mediaScopeImagesOnly) {
		t.Fatal("images-only scope must ignore file and audio")
	}
	if !toolOutputContainsMedia(file, mediaScopeAllSupported) || !toolOutputContainsMedia(audio, mediaScopeAllSupported) {
		t.Fatal("all-supported scope must accept file and audio")
	}
}

func TestClampPreservesLongTextButRemovesDataAndBase64Payloads(t *testing.T) {
	longText := strings.Repeat("ordinary text ", 9000) + " with spaces and punctuation!"
	dataURL := largeImageDataURL()
	base64Payload := base64.StdEncoding.EncodeToString(makeClampBytes(18000))
	value := any(map[string]any{
		"text":     longText,
		"data_url": dataURL,
		"raw":      base64Payload,
	})

	clampBase64ishStrings(&value)

	typed := value.(map[string]any)
	if typed["text"] != longText {
		t.Fatal("long text must be preserved")
	}
	for _, key := range []string{"data_url", "raw"} {
		if got, _ := typed[key].(string); !strings.HasPrefix(got, "[cc-switch: omitted ") {
			t.Fatalf("%s = %q", key, got)
		}
	}
}

// makeClampBytes produces an 0x00-0xFF cycling payload like the Rust test.
func makeClampBytes(n int) []byte {
	bytes := make([]byte, n)
	for i := range bytes {
		bytes[i] = byte(i)
	}
	return bytes
}

func TestNoMediaStripIsByteStable(t *testing.T) {
	value := any(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "hello"},
			map[string]any{"type": "image", "name": "business metadata"},
		},
	})
	before := canonicalJSONString(value)
	replacement := map[string]any{"type": "text", "text": "moved"}
	var media []any

	replaced := stripMediaFromToolValue(&value, &media, mediaScopeAllSupported, replacement, "moved")
	if replaced != 0 {
		t.Fatalf("replaced = %d", replaced)
	}
	if len(media) != 0 {
		t.Fatalf("media = %v", media)
	}
	if canonicalJSONString(value) != before {
		t.Fatal("byte stability broken")
	}
}
