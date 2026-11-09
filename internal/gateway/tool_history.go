package gateway

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
)

const historicalToolPrompt = "Historical tool calls appear as assistant tool_calls; their outputs are provided\nin user messages tagged [tool_result id=... name=...]. Treat tagged messages as\ntool outputs, not as new user requests."

func toolTagValue(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n[]\"\\") {
		return strconv.Quote(value)
	}
	return value
}

func transformHistoricalToolReplies(path string, payload M, enabled bool) bool {
	if !enabled {
		return false
	}
	switch path {
	case "/v1/chat/completions":
		return transformChatToolHistory(payload)
	case "/v1/messages", "/v1/messages/count_tokens":
		return transformMessagesToolHistory(payload)
	case "/v1/responses":
		return transformResponsesToolHistory(payload)
	}
	return false
}

// The trailing group is the current tool round; earlier results are history.
func transformChatToolHistory(payload M) bool {
	messages := arr(payload["messages"])
	end := len(messages)
	for end > 0 && obj(messages[end-1])["role"] == "tool" {
		end--
	}
	names := map[string]string{}
	changed := false
	for _, item := range messages[:end] {
		message := obj(item)
		if message["role"] == "assistant" {
			for _, call := range arr(message["tool_calls"]) {
				c := obj(call)
				names[text(c["id"])] = text(obj(c["function"])["name"])
			}
		}
		if message["role"] != "tool" {
			continue
		}
		id := text(message["tool_call_id"])
		name := names[id]
		if name == "" {
			name = text(message["name"])
		}
		tag := "[tool_result id=" + toolTagValue(id) + " name=" + toolTagValue(name) + "]"
		switch content := message["content"].(type) {
		case string:
			message["content"] = tag + "\n" + content
		case []any:
			message["content"] = append([]any{M{"type": "text", "text": tag}}, content...)
		case nil:
			message["content"] = tag
		default:
			continue
		}
		message["role"] = "user"
		delete(message, "tool_call_id")
		delete(message, "name")
		changed = true
	}
	if !changed {
		return false
	}
	// Prefer developer instructions, then system instructions. Append once and
	// preserve content blocks and their metadata, including non-text blocks.
	for _, role := range []string{"developer", "system"} {
		for i := len(messages) - 1; i >= 0; i-- {
			message := obj(messages[i])
			if message["role"] != role {
				continue
			}
			switch content := message["content"].(type) {
			case string:
				if !strings.Contains(content, historicalToolPrompt) {
					message["content"] = content + "\n\n" + historicalToolPrompt
				}
				return true
			case []any:
				for _, block := range content {
					b := obj(block)
					if b["type"] == "text" || b["type"] == "input_text" {
						if strings.Contains(text(b["text"]), historicalToolPrompt) {
							return true
						}
					}
				}
				message["content"] = append(content, M{"type": "text", "text": historicalToolPrompt})
				return true
			}
		}
	}
	payload["messages"] = append([]any{M{"role": "system", "content": historicalToolPrompt}}, messages...)
	return true
}

const messagesToolPrompt = "Historical tool calls appear as assistant tool_use blocks; their outputs are provided\nin user text blocks tagged [tool_result id=...]. Treat tagged blocks as\ntool outputs, not as new user requests."
const responsesToolPrompt = "Historical tool calls appear as function_call items; their outputs are provided\nin user messages tagged [tool_result id=...]. Treat tagged messages as\ntool outputs, not as new user requests."

func appendToolHistoryPrompt(payload M, key, prompt, blockType string) {
	switch content := payload[key].(type) {
	case string:
		if !strings.Contains(content, prompt) {
			if content != "" {
				content += "\n\n"
			}
			payload[key] = content + prompt
		}
	case []any:
		for _, item := range content {
			if strings.Contains(text(obj(item)["text"]), prompt) {
				return
			}
		}
		payload[key] = append(content, M{"type": blockType, "text": prompt})
	default:
		payload[key] = prompt
	}
}

// Tag the result while retaining native text/image/file blocks in their order.
func taggedToolContent(tag string, content any, blockType string) []any {
	switch value := content.(type) {
	case string:
		return []any{M{"type": blockType, "text": tag + "\n" + value}}
	case []any:
		return append([]any{M{"type": blockType, "text": tag}}, value...)
	case nil:
		return []any{M{"type": blockType, "text": tag}}
	default:
		raw, _ := json.Marshal(value)
		return []any{M{"type": blockType, "text": tag + "\n" + string(raw)}}
	}
}

func hasMessagesToolResult(message M) bool {
	if message["role"] != "user" {
		return false
	}
	for _, block := range arr(message["content"]) {
		if obj(block)["type"] == "tool_result" {
			return true
		}
	}
	return false
}

func transformMessagesToolHistory(payload M) bool {
	messages := arr(payload["messages"])
	end := len(messages)
	// Parallel results may share a user message with ordinary text. Preserve
	// every result in the trailing group of result-bearing user messages.
	for end > 0 && hasMessagesToolResult(obj(messages[end-1])) {
		end--
	}
	changed := false
	for _, item := range messages[:end] {
		message := obj(item)
		if !hasMessagesToolResult(message) {
			continue
		}
		blocks := []any{}
		for _, block := range arr(message["content"]) {
			result := obj(block)
			if result["type"] != "tool_result" {
				blocks = append(blocks, block)
				continue
			}
			tag := "[tool_result id=" + toolTagValue(text(result["tool_use_id"])) + "]"
			if yes(result["is_error"]) {
				tag += "\n[is_error=true]"
			}
			converted := taggedToolContent(tag, result["content"], "text")
			if cache, ok := result["cache_control"]; ok {
				// The cache boundary belongs after the entire result, not its tag.
				last := maps.Clone(obj(converted[len(converted)-1]))
				last["cache_control"] = cache
				converted[len(converted)-1] = last
			}
			blocks = append(blocks, converted...)
			changed = true
		}
		message["content"] = blocks
	}
	if changed {
		appendToolHistoryPrompt(payload, "system", messagesToolPrompt, "text")
	}
	return changed
}

func transformResponsesToolHistory(payload M) bool {
	items := arr(payload["input"])
	end := len(items)
	for end > 0 && obj(items[end-1])["type"] == "function_call_output" {
		end--
	}
	changed := false
	for i, item := range items[:end] {
		output := obj(item)
		if output["type"] != "function_call_output" {
			continue
		}
		tag := "[tool_result id=" + toolTagValue(text(output["call_id"])) + "]"
		items[i] = M{"type": "message", "role": "user", "content": taggedToolContent(tag, output["output"], "input_text")}
		changed = true
	}
	if changed {
		appendToolHistoryPrompt(payload, "instructions", responsesToolPrompt, "input_text")
	}
	return changed
}
