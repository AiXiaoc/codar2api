package gateway

import "strings"

const (
	codarIdentity = "You are an AI agent running on the Codar runtime."
	legacyFill    = ".\u200b"
	identityFill  = legacyFill + "X"
)

type identityPart struct {
	value string
	set   func(string)
}

// identityParts collects a prompt's leading text blocks without crossing a
// non-text block. Setters preserve each block and its other fields.
func identityParts(value any, assign func(string)) []identityPart {
	if s, ok := value.(string); ok {
		return []identityPart{{value: s, set: assign}}
	}
	blocks, ok := value.([]any)
	if !ok {
		return nil
	}
	parts := []identityPart{}
	for i, item := range blocks {
		if s, ok := item.(string); ok {
			index := i
			parts = append(parts, identityPart{value: s, set: func(v string) { blocks[index] = v }})
			continue
		}
		block, ok := item.(map[string]any)
		if ok && (block["type"] == "text" || block["type"] == "input_text") {
			if s, ok := block["text"].(string); ok {
				parts = append(parts, identityPart{value: s, set: func(v string) { block["text"] = v }})
				continue
			}
		}
		if len(parts) > 0 {
			break
		}
	}
	return parts
}

// transformIdentity adds the identity line and optional fill at the start of
// a prompt, preserving existing text and text block metadata.
func transformIdentity(container M, key string, rewrite, fill bool) bool {
	value, ok := container[key]
	if !ok {
		return false
	}
	parts := identityParts(value, func(v string) { container[key] = v })
	if len(parts) == 0 {
		return false
	}
	var start strings.Builder
	for _, part := range parts {
		start.WriteString(part.value)
		if start.Len() >= len(identityFill)+len(codarIdentity)+1 {
			break
		}
	}
	prefix := start.String()
	offset := 0
	if strings.HasPrefix(prefix, identityFill) {
		offset = len(identityFill)
	} else if strings.HasPrefix(prefix, legacyFill) {
		offset = len(legacyFill)
	}
	insert := func(at int, value string) {
		for i := range parts {
			if at <= len(parts[i].value) {
				parts[i].value = parts[i].value[:at] + value + parts[i].value[at:]
				return
			}
			at -= len(parts[i].value)
		}
	}
	changed := false
	if rewrite && !strings.HasPrefix(prefix[offset:], codarIdentity+"\n") {
		insert(offset, codarIdentity+"\n")
		changed = true
	}
	if fill {
		switch offset {
		case 0:
			insert(0, identityFill)
			changed = true
		case len(legacyFill):
			insert(offset, "X")
			changed = true
		}
	}
	if changed {
		for _, part := range parts {
			part.set(part.value)
		}
	}
	return changed
}

// transformIdentityPrompts applies the options to system and developer
// instructions in each supported model API, leaving user and tool data alone.
func transformIdentityPrompts(path string, payload M, rewrite, fill bool) bool {
	if !rewrite && !fill {
		return false
	}
	changed := false
	apply := func(container M, key string) {
		if transformIdentity(container, key, rewrite, fill) {
			changed = true
		}
	}
	applyMessages := func(items any) {
		for _, item := range arr(items) {
			message := obj(item)
			if message["role"] == "system" || message["role"] == "developer" {
				apply(message, "content")
			}
		}
	}
	switch path {
	case "/v1/chat/completions":
		applyMessages(payload["messages"])
	case "/v1/responses":
		apply(payload, "instructions")
		applyMessages(payload["input"])
	case "/v1/messages", "/v1/messages/count_tokens":
		apply(payload, "system")
		applyMessages(payload["messages"])
	}
	return changed
}
