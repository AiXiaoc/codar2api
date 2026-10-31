package gateway

import "strings"

// normalizeHarnessIdentity preserves the developer's instructions while using
// a neutral identity in the standard OpenAI-compatible chat request.
func normalizeHarnessIdentity(payload M) {
	const prefix = "You are an AI agent powered by DeepSeek Harness."
	for _, item := range arr(payload["messages"]) {
		message := obj(item)
		if message["role"] != "developer" && message["role"] != "system" {
			continue
		}
		content, ok := message["content"].(string)
		if ok && strings.HasPrefix(content, prefix) {
			message["content"] = "You are an AI coding agent." + strings.TrimPrefix(content, prefix)
		}
	}
}
