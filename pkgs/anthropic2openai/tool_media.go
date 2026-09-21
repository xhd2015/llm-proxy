package anthropic2openai

import (
	"strconv"
	"strings"
)

// Shared media handling for tool outputs, ported from cc-switch
// proxy/tool_media.rs (the subset the Anthropic bridge uses: recognition,
// stripping, and clamping of media payloads embedded in tool results).
//
// Responses and Anthropic tool outputs may carry structured media blocks.
// The bridge extracts those blocks so the text-only portions of a tool result
// stay clean, and clamps residual base64 payloads so oversized binaries are
// not re-sent inside tool arguments.

const (
	wholeDataURLMinBytes       = 8 * 1024
	base64ishMinBytes          = 16 * 1024
	maxMediaTraversalDepth     = 32
	toolResultMediaMovedMarker = "[cc-switch: tool result media moved to the following user message]"
	// toolResultMediaAttachedMarker replaces media blocks that stay attached
	// to the tool result instead of moving to a separate user message.
	toolResultMediaAttachedMarker = "[cc-switch: tool result media attached as native media]"
)

type toolMediaScope int

const (
	// mediaScopeImagesOnly is used by image-capability sanitizers.
	mediaScopeImagesOnly toolMediaScope = iota
	// mediaScopeInlineImagesOnly only accepts inline base64 image data.
	mediaScopeInlineImagesOnly
	// mediaScopeAllSupported accepts every mapped modality.
	mediaScopeAllSupported
)

type toolMediaKind int

const (
	toolMediaKindImage toolMediaKind = iota
	toolMediaKindFile
	toolMediaKindAudio
)

func (scope toolMediaScope) allows(kind toolMediaKind) bool {
	return kind == toolMediaKindImage || scope == mediaScopeAllSupported
}

// scopeAcceptsChatPart narrows the inline-images scope to image parts whose
// URL carries inline base64 data (remote URLs and malformed data URLs are
// rejected).
func scopeAcceptsChatPart(scope toolMediaScope, part map[string]any) bool {
	if scope != mediaScopeInlineImagesOnly {
		return true
	}
	return chatImagePartHasInlineData(part)
}

func chatImagePartHasInlineData(part map[string]any) bool {
	imageURL, _ := part["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	trimmed := strings.TrimSpace(url)
	commaIndex := strings.Index(trimmed, ",")
	if commaIndex < 0 {
		return false
	}
	return commaIndex+1 < len(trimmed) && isImageBase64DataURL(trimmed)
}

// stripAndClampMediaFromToolValue extracts recognized media blocks and
// replaces them in place, then clamps residual large data/base64 scalars on
// media-bearing outputs. Parseable JSON strings are clamped while still
// represented as a JSON tree before being canonicalized back into their
// original string container. Ported from cc-switch tool_media.rs
// strip_and_clamp_media_from_tool_value.
func stripAndClampMediaFromToolValue(value *any, mediaParts *[]any, scope toolMediaScope, replacementBlock map[string]any, replacementText string) int {
	replaced := stripMediaFromToolValueAtDepth(value, mediaParts, scope, replacementBlock, replacementText, true, 0)
	if replaced > 0 {
		clampBase64ishStrings(value)
	}
	return replaced
}

// stripMediaFromToolValue is the variant without residual-base64 clamping.
func stripMediaFromToolValue(value *any, mediaParts *[]any, scope toolMediaScope, replacementBlock map[string]any, replacementText string) int {
	return stripMediaFromToolValueAtDepth(value, mediaParts, scope, replacementBlock, replacementText, false, 0)
}

// toolOutputContainsMedia is read-only media detection using the same shape
// classifier and recursive boundaries as stripMediaFromToolValue.
func toolOutputContainsMedia(value any, scope toolMediaScope) bool {
	return toolOutputContainsMediaAtDepth(value, scope, 0)
}

func toolOutputContainsMediaAtDepth(value any, scope toolMediaScope, depth int) bool {
	if depth > maxMediaTraversalDepth {
		return false
	}
	switch typed := value.(type) {
	case string:
		if scope.allows(toolMediaKindImage) {
			if _, ok := wholeStringImageDataURL(typed); ok {
				return true
			}
		}
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false
		}
		parsed, err := decodeJSON(trimmed)
		if err != nil {
			return false
		}
		return toolOutputContainsMediaAtDepth(parsed, scope, depth+1)
	case []any:
		for _, item := range typed {
			if toolOutputContainsMediaAtDepth(item, scope, depth+1) {
				return true
			}
		}
		return false
	case map[string]any:
		if _, ok := chatMediaPartFromToolPart(typed, scope); ok {
			return true
		}
		if content, hasContent := typed["content"]; hasContent {
			return toolOutputContainsMediaAtDepth(content, scope, depth+1)
		}
		return false
	default:
		return false
	}
}

// clampBase64ishStrings removes residual data/base64 payloads only after a
// tool output has already been positively identified as media-bearing.
// Ordinary long text is kept.
func clampBase64ishStrings(value *any) {
	switch typed := (*value).(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		shouldOmit := (len(trimmed) >= wholeDataURLMinBytes && hasCaseInsensitivePrefix(trimmed, "data:")) ||
			looksLikeBase64Payload(trimmed)
		if shouldOmit {
			*value = "[cc-switch: omitted " + strconv.Itoa(len(typed)) + " bytes]"
		}
	case []any:
		for i := range typed {
			clampBase64ishStrings(&typed[i])
		}
	case map[string]any:
		for key, nested := range typed {
			clampBase64ishStrings(&nested)
			typed[key] = nested
		}
	}
}

func stripMediaFromToolValueAtDepth(value *any, mediaParts *[]any, scope toolMediaScope, replacementBlock map[string]any, replacementText string, clampParsedStrings bool, depth int) int {
	if depth > maxMediaTraversalDepth {
		return 0
	}
	switch typed := (*value).(type) {
	case string:
		if scope.allows(toolMediaKindImage) {
			if mediaPart, ok := wholeStringImageDataURL(typed); ok {
				*mediaParts = append(*mediaParts, mediaPart)
				*value = replacementText
				return 1
			}
		}
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		parsed, err := decodeJSON(trimmed)
		if err != nil {
			return 0
		}
		replaced := stripMediaFromToolValueAtDepth(&parsed, mediaParts, scope, replacementBlock, replacementText, clampParsedStrings, depth+1)
		if replaced > 0 {
			if clampParsedStrings {
				clampBase64ishStrings(&parsed)
			}
			*value = canonicalJSONString(parsed)
		}
		return replaced
	case []any:
		total := 0
		for i := range typed {
			total += stripMediaFromToolValueAtDepth(&typed[i], mediaParts, scope, replacementBlock, replacementText, clampParsedStrings, depth+1)
		}
		return total
	case map[string]any:
		if mediaPart, ok := chatMediaPartFromToolPart(typed, scope); ok {
			*mediaParts = append(*mediaParts, mediaPart)
			*value = cloneMapValue(replacementBlock)
			return 1
		}
		if content, hasContent := typed["content"]; hasContent {
			return stripMediaFromToolValueAtDepth(&content, mediaParts, scope, replacementBlock, replacementText, clampParsedStrings, depth+1)
		}
		return 0
	default:
		return 0
	}
}

// chatMediaPartFromToolPart recognizes one complete media block and converts
// it to a Chat-shaped user content part (the extraction target shape).
func chatMediaPartFromToolPart(part map[string]any, scope toolMediaScope) (map[string]any, bool) {
	kind, ok := toolMediaKindOf(part)
	if !ok || !scope.allows(kind) {
		return nil, false
	}
	switch kind {
	case toolMediaKindImage:
		imagePart, ok := chatImagePart(part)
		if !ok {
			return nil, false
		}
		if !scopeAcceptsChatPart(scope, imagePart) {
			return nil, false
		}
		return imagePart, true
	case toolMediaKindFile:
		file, ok := chatFileFromInputFile(part)
		if !ok {
			return nil, false
		}
		return map[string]any{"type": "file", "file": file}, true
	case toolMediaKindAudio:
		inputAudio, exists := part["input_audio"].(map[string]any)
		if !exists {
			return nil, false
		}
		return map[string]any{"type": "input_audio", "input_audio": inputAudio}, true
	}
	return nil, false
}

// chatFileFromInputFile maps a Responses `input_file` block to the file
// payload shared by top-level content and tool-output extraction.
func chatFileFromInputFile(part map[string]any) (map[string]any, bool) {
	_, hasFileID := part["file_id"]
	_, hasFileData := part["file_data"]
	if !hasFileID && !hasFileData {
		return nil, false
	}
	file := map[string]any{}
	for _, key := range []string{"file_id", "file_data", "filename"} {
		if value, exists := part[key]; exists {
			file[key] = value
		}
	}
	return file, true
}

// wholeStringImageDataURL recognizes a complete image data URL stored as a
// scalar string. Only whole-string matches are accepted; embedded data URLs in
// HTML/CSS/SVG source are deliberately left alone, and small values remain
// text.
func wholeStringImageDataURL(value string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < wholeDataURLMinBytes || !isImageBase64DataURL(trimmed) {
		return nil, false
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": trimmed},
	}, true
}

func toolMediaKindOf(part map[string]any) (toolMediaKind, bool) {
	partType, hasType := part["type"].(string)
	if !hasType {
		if _, ok := looseDataImageURL(part); ok {
			return toolMediaKindImage, true
		}
		return 0, false
	}
	switch partType {
	case "input_image", "image_url":
		if _, ok := normalizedImageURL(part); ok {
			return toolMediaKindImage, true
		}
	case "input_file":
		_, hasFileID := part["file_id"]
		_, hasFileData := part["file_data"]
		if hasFileID || hasFileData {
			return toolMediaKindFile, true
		}
	case "input_audio":
		if _, ok := part["input_audio"].(map[string]any); ok {
			return toolMediaKindAudio, true
		}
	case "image":
		if typedImageHasPayload(part) {
			return toolMediaKindImage, true
		}
	}
	return 0, false
}

func chatImagePart(part map[string]any) (map[string]any, bool) {
	partType, _ := part["type"].(string)
	switch partType {
	case "input_image", "image_url":
		imageURL, ok := normalizedImageURL(part)
		if !ok {
			return nil, false
		}
		return imageURLContentPart(imageURL), true
	case "image":
		imageURL, ok := typedImageURL(part)
		if !ok {
			return nil, false
		}
		return imageURLContentPart(imageURL), true
	default:
		if imageURL, ok := looseDataImageURL(part); ok {
			return imageURLContentPart(imageURL), true
		}
		return nil, false
	}
}

func normalizedImageURL(part map[string]any) (map[string]any, bool) {
	imageURL, exists := part["image_url"]
	if !exists {
		return nil, false
	}
	switch typed := imageURL.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
		object := map[string]any{"url": typed}
		mergeTopLevelDetail(part, object)
		return object, true
	case map[string]any:
		url, _ := typed["url"].(string)
		if strings.TrimSpace(url) == "" {
			return nil, false
		}
		object := cloneMapValue(typed)
		mergeTopLevelDetail(part, object)
		return object, true
	default:
		return nil, false
	}
}

func looseDataImageURL(part map[string]any) (map[string]any, bool) {
	if _, hasType := part["type"]; hasType {
		return nil, false
	}
	normalized, ok := normalizedImageURL(part)
	if !ok {
		return nil, false
	}
	url, _ := normalized["url"].(string)
	if !hasCaseInsensitivePrefix(url[:minInt(5, len(url))], "data:") {
		return nil, false
	}
	return normalized, true
}

func typedImageHasPayload(part map[string]any) bool {
	if source, ok := part["source"].(map[string]any); ok {
		if sourceMediaTypeIsImage(source) {
			url, _ := source["url"].(string)
			data, _ := source["data"].(string)
			if strings.TrimSpace(url) != "" || data != "" {
				return true
			}
		}
	}
	data, _ := part["data"].(string)
	if data == "" {
		return false
	}
	mimeType, ok := part["mimeType"].(string)
	if !ok {
		mimeType, ok = part["mime_type"].(string)
	}
	return ok && isImageMimeType(mimeType)
}

func typedImageURL(part map[string]any) (map[string]any, bool) {
	if source, ok := part["source"].(map[string]any); ok {
		if !sourceMediaTypeIsImage(source) {
			return nil, false
		}
		if url, _ := source["url"].(string); strings.TrimSpace(url) != "" {
			imageURL := map[string]any{"url": url}
			mergeTopLevelDetail(part, imageURL)
			return imageURL, true
		}
		if data, _ := source["data"].(string); data != "" {
			mediaType := firstString(source, "media_type", "mime_type", "mimeType")
			if mediaType == "" {
				mediaType = "image/png"
			}
			url := data
			if !hasCaseInsensitivePrefix(data[:minInt(11, len(data))], "data:image/") {
				url = "data:" + mediaType + ";base64," + data
			}
			imageURL := map[string]any{"url": url}
			mergeTopLevelDetail(part, imageURL)
			return imageURL, true
		}
	}

	data, _ := part["data"].(string)
	if data == "" {
		return nil, false
	}
	mediaType, ok := firstStringOk(part, "mimeType", "mime_type")
	if !ok || !isImageMimeType(mediaType) {
		return nil, false
	}
	imageURL := map[string]any{"url": "data:" + mediaType + ";base64," + data}
	mergeTopLevelDetail(part, imageURL)
	return imageURL, true
}

func imageURLContentPart(imageURL map[string]any) map[string]any {
	return map[string]any{"type": "image_url", "image_url": imageURL}
}

func mergeTopLevelDetail(part map[string]any, imageURL map[string]any) {
	if _, hasDetail := imageURL["detail"]; !hasDetail {
		if detail, exists := part["detail"]; exists {
			imageURL["detail"] = detail
		}
	}
}

func sourceMediaTypeIsImage(source map[string]any) bool {
	mediaType, ok := firstStringOk(source, "media_type", "mime_type", "mimeType")
	if !ok {
		return true
	}
	return isImageMimeType(mediaType)
}

func isImageMimeType(value string) bool {
	return len(value) >= 6 && hasCaseInsensitivePrefix(value[:6], "image/")
}

func isImageBase64DataURL(value string) bool {
	commaIndex := strings.Index(value, ",")
	if commaIndex < 0 {
		return false
	}
	header := strings.ToLower(value[:commaIndex])
	return strings.HasPrefix(header, "data:image/") && strings.HasSuffix(header, ";base64")
}

func looksLikeBase64Payload(value string) bool {
	if len(value) < base64ishMinBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '+', b == '/', b == '=':
		default:
			return false
		}
	}
	return true
}

func hasCaseInsensitivePrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}

func firstString(value map[string]any, keys ...string) string {
	result, _ := firstStringOk(value, keys...)
	return result
}

func firstStringOk(value map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if result, ok := value[key].(string); ok {
			return result, true
		}
	}
	return "", false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
