package anthropic2openai

import (
	"encoding/json"
	"strings"
)

// Codex tool context: mirrors the Rust CodexToolContext from
// cc-switch providers/transform_codex_chat.rs (the subset the Anthropic bridge
// shares with the chat bridge). Function, namespace, custom, tool_search, and
// dynamically loaded tools all receive stable flat names upstream.

const (
	toolSearchProxyName        = "tool_search"
	customToolInputField       = "input"
	chatToolNameMaxLen         = 64
	customToolInputDescription = "Raw string input for the original custom tool. Preserve formatting exactly and follow the original tool definition embedded in the description."
	customToolMetadataHeading  = "Original tool definition:"
)

type codexToolKind int

const (
	codexToolKindFunction codexToolKind = iota
	codexToolKindNamespace
	codexToolKindCustom
	codexToolKindToolSearch
)

type codexToolSpec struct {
	kind      codexToolKind
	name      string
	namespace string // empty when absent
}

type namespacedName struct {
	namespace string
	name      string
}

type codexToolContext struct {
	chatTools               []any
	seenChatNames           map[string]bool
	chatNameToSpec          map[string]codexToolSpec
	namespaceNameToChatName map[namespacedName]string
}

func newCodexToolContext() *codexToolContext {
	return &codexToolContext{
		seenChatNames:           map[string]bool{},
		chatNameToSpec:          map[string]codexToolSpec{},
		namespaceNameToChatName: map[namespacedName]string{},
	}
}

func (c *codexToolContext) lookupChatName(chatName string) (codexToolSpec, bool) {
	spec, ok := c.chatNameToSpec[chatName]
	return spec, ok
}

func (c *codexToolContext) isCustomToolChatName(chatName string) bool {
	spec, ok := c.lookupChatName(chatName)
	return ok && spec.kind == codexToolKindCustom
}

func (c *codexToolContext) chatNameForResponseFunction(name, namespace string) string {
	if namespace != "" {
		if chatName, ok := c.namespaceNameToChatName[namespacedName{namespace, name}]; ok {
			return chatName
		}
		return flattenNamespaceToolName(namespace, name)
	}
	return name
}

func (c *codexToolContext) addChatTool(chatName string, spec codexToolSpec, chatTool map[string]any) {
	if strings.TrimSpace(chatName) == "" || c.seenChatNames[chatName] {
		return
	}
	c.seenChatNames[chatName] = true
	if spec.namespace != "" {
		c.namespaceNameToChatName[namespacedName{spec.namespace, spec.name}] = chatName
	}
	c.chatNameToSpec[chatName] = spec
	c.chatTools = append(c.chatTools, chatTool)
}

func (c *codexToolContext) addFunctionTool(tool map[string]any, namespace string) {
	originalName, ok := responsesToolName(tool)
	if !ok {
		return
	}
	chatName := originalName
	if namespace != "" {
		chatName = flattenNamespaceToolName(namespace, originalName)
	}

	chatTool, ok := responsesFunctionToolToChatTool(tool, chatName)
	if !ok {
		return
	}
	kind := codexToolKindFunction
	if namespace != "" {
		kind = codexToolKindNamespace
	}
	c.addChatTool(chatName, codexToolSpec{kind: kind, name: originalName, namespace: namespace}, chatTool)
}

func (c *codexToolContext) addCustomTool(tool map[string]any) {
	name, ok := responsesToolName(tool)
	if !ok {
		return
	}
	chatTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": responsesCustomToolDescription(tool),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					customToolInputField: map[string]any{
						"type":        "string",
						"description": customToolInputDescription,
					},
				},
				"required": []any{customToolInputField},
			},
		},
	}
	c.addChatTool(name, codexToolSpec{kind: codexToolKindCustom, name: name}, chatTool)
}

func (c *codexToolContext) addToolSearchTool() {
	chatTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        toolSearchProxyName,
			"description": "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query for tools or connectors to load.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of tool groups to return.",
					},
				},
				"required": []any{"query"},
			},
		},
	}
	c.addChatTool(toolSearchProxyName, codexToolSpec{kind: codexToolKindToolSearch, name: toolSearchProxyName}, chatTool)
}

func (c *codexToolContext) addNamespaceTool(namespaceTool map[string]any) {
	namespace, _ := namespaceTool["name"].(string)
	if namespace == "" {
		return
	}
	children, ok := namespaceTool["tools"].([]any)
	if !ok {
		children, ok = namespaceTool["children"].([]any)
		if !ok {
			return
		}
	}
	for _, child := range children {
		if typed, ok := child.(map[string]any); ok {
			if kind, _ := typed["type"].(string); kind == "function" {
				c.addFunctionTool(typed, namespace)
			}
		}
	}
}

func (c *codexToolContext) addResponseTool(tool any) {
	switch typed := tool.(type) {
	case string:
		c.addCustomTool(map[string]any{"type": "custom", "name": typed})
	case map[string]any:
		switch kind, _ := typed["type"].(string); kind {
		case "function":
			c.addFunctionTool(typed, "")
		case "custom":
			c.addCustomTool(typed)
		case "tool_search":
			c.addToolSearchTool()
		case "namespace":
			c.addNamespaceTool(typed)
		}
	}
}

// buildCodexToolContextFromRequest indexes the request's tools array plus any
// tools delivered by prior tool_search output items.
func buildCodexToolContextFromRequest(body map[string]any) *codexToolContext {
	context := newCodexToolContext()
	if tools, ok := body["tools"].([]any); ok {
		for _, tool := range tools {
			context.addResponseTool(tool)
		}
	}
	if input, ok := body["input"]; ok {
		collectToolSearchOutputTools(input, context)
	}
	return context
}

func collectToolSearchOutputTools(value any, context *codexToolContext) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectToolSearchOutputTools(item, context)
		}
	case map[string]any:
		if kind, _ := typed["type"].(string); kind == "tool_search_output" {
			if tools, ok := typed["tools"].([]any); ok {
				for _, tool := range tools {
					context.addResponseTool(tool)
				}
			}
		}
		for _, nested := range typed {
			collectToolSearchOutputTools(nested, context)
		}
	}
}

func flattenNamespaceToolName(namespace, name string) string {
	fullName := namespace + "__" + name
	if len(fullName) <= chatToolNameMaxLen {
		return fullName
	}
	hash := shortSHA256Hex([]byte(fullName))
	suffix := "__" + hash
	prefixLen := chatToolNameMaxLen - len(suffix)
	if prefixLen < 0 {
		prefixLen = 0
	}
	// Trim on rune boundaries so multi-byte names stay valid UTF-8.
	prefix := fullName
	if prefixLen < len(prefix) {
		for prefixLen > 0 && !isRuneStart(prefix[prefixLen]) {
			prefixLen--
		}
		prefix = prefix[:prefixLen]
	}
	return prefix + suffix
}

func isRuneStart(b byte) bool {
	return b < 0x80 || b >= 0xC0
}

func responsesToolName(tool map[string]any) (string, bool) {
	name := ""
	if function, ok := tool["function"].(map[string]any); ok {
		name, _ = function["name"].(string)
	}
	if name == "" {
		name, _ = tool["name"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	return name, true
}

func responsesCustomToolDescription(tool map[string]any) string {
	var description strings.Builder
	description.WriteString(customToolMetadataHeading)
	description.WriteString("\n```json\n")
	// Keep the embedded definition compact while remaining stable across map
	// storage order.
	description.WriteString(canonicalJSONString(tool))
	description.WriteString("\n```")
	return description.String()
}

// normalizeFunctionParameters ensures a function's `parameters` JSON Schema has
// type "object" (some Responses tools carry null parameters; strict upstreams
// require {"type":"object","properties":{...}}).
func normalizeFunctionParameters(params any) map[string]any {
	var result map[string]any
	if typed, ok := params.(map[string]any); ok {
		result = cloneMapValue(typed)
	} else {
		result = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if kind, _ := result["type"].(string); kind != "object" {
		result["type"] = "object"
	}
	return result
}

func responsesFunctionToolToChatTool(tool map[string]any, chatName string) (map[string]any, bool) {
	if kind, _ := tool["type"].(string); kind != "function" {
		return nil, false
	}

	if function, ok := tool["function"].(map[string]any); ok {
		functionCopy := cloneMapValue(function)
		functionCopy["parameters"] = normalizeFunctionParameters(functionCopy["parameters"])
		functionCopy["name"] = chatName
		if strict, exists := tool["strict"]; exists {
			if _, hasStrict := functionCopy["strict"]; !hasStrict {
				functionCopy["strict"] = strict
			}
		}
		return map[string]any{"type": "function", "function": functionCopy}, true
	}

	function := map[string]any{
		"name":        chatName,
		"description": orNull(tool["description"]),
		"parameters":  normalizeFunctionParameters(tool["parameters"]),
	}
	if strict, exists := tool["strict"]; exists {
		function["strict"] = strict
	}
	return map[string]any{"type": "function", "function": function}, true
}

// ---------------------------------------------------------------------------
// Responses tool-call item builders (from cc-switch codex_chat_common.rs and
// transform_codex_chat.rs — the shapes the Anthropic bridge emits for tool_use
// blocks).
// ---------------------------------------------------------------------------

func attachOptionalReasoningContentField(item map[string]any, reasoning string) bool {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return false
	}
	item["reasoning_content"] = reasoning
	return true
}

func responseFunctionCallItem(itemID, status, callID, name, arguments, reasoning string) map[string]any {
	item := map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
	attachOptionalReasoningContentField(item, reasoning)
	return item
}

func responseFunctionCallItemWithNamespace(itemID, status, callID, name, namespace, arguments, reasoning string) map[string]any {
	item := responseFunctionCallItem(itemID, status, callID, name, arguments, reasoning)
	if namespace != "" {
		item["namespace"] = namespace
	}
	return item
}

func responseToolCallItemIDFromChatName(callID, chatName string, toolContext *codexToolContext) string {
	if toolContext.isCustomToolChatName(chatName) {
		return "ctc_" + callID
	}
	return "fc_" + callID
}

func responseToolCallItemFromChatName(itemID, status, callID, chatName, arguments, reasoning string, toolContext *codexToolContext) map[string]any {
	spec, ok := toolContext.lookupChatName(chatName)
	if !ok {
		return responseFunctionCallItem(itemID, status, callID, chatName, arguments, reasoning)
	}
	switch spec.kind {
	case codexToolKindToolSearch:
		return responseToolSearchCallItem(callID, status, arguments, reasoning)
	case codexToolKindCustom:
		return responseCustomToolCallItem(itemID, status, callID, spec.name, arguments, reasoning)
	default:
		return responseFunctionCallItemWithNamespace(itemID, status, callID, spec.name, spec.namespace, arguments, reasoning)
	}
}

func responseToolSearchCallItem(callID, status, arguments, reasoning string) map[string]any {
	item := map[string]any{
		"type":      "tool_search_call",
		"call_id":   callID,
		"status":    status,
		"execution": "client",
		"arguments": parseToolArgumentsObject(arguments),
	}
	attachOptionalReasoningContentField(item, reasoning)
	return item
}

func responseCustomToolCallItem(itemID, status, callID, name, arguments, reasoning string) map[string]any {
	item := map[string]any{
		"id":      itemID,
		"type":    "custom_tool_call",
		"status":  status,
		"call_id": callID,
		"name":    name,
		"input":   customToolInputFromChatArguments(arguments),
	}
	attachOptionalReasoningContentField(item, reasoning)
	return item
}

func parseToolArgumentsObject(arguments string) map[string]any {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return map[string]any{"query": arguments}
	}
	if typed, ok := parsed.(map[string]any); ok {
		return typed
	}
	return map[string]any{"query": arguments}
}

func customToolInputFromChatArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return arguments
	}
	typed, ok := parsed.(map[string]any)
	if !ok {
		return arguments
	}
	if input, ok := typed[customToolInputField].(string); ok {
		return input
	}
	return arguments
}

// cloneMapValue deep-copies a JSON map (Go maps are references; the Rust code
// clones values liberally and callers mutate their copies).
func cloneMapValue(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, item := range value {
		clone[key] = cloneValue(item)
	}
	return clone
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMapValue(typed)
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = cloneValue(item)
		}
		return clone
	default:
		return value
	}
}

func orNull(value any) any {
	if value == nil {
		return nil
	}
	return value
}
