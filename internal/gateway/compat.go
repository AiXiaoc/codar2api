package gateway

import "strings"

const (
	harnessIdentity = "You are an AI agent powered by DeepSeek Harness."
	neutralIdentity = "You are an AI coding agent."
	identityFill    = ".\u200b"
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

// transformIdentity changes only the start of a prompt. The fixed identity
// may span adjacent text blocks; text after it stays in its original block.
func transformIdentity(container M, key string, rewrite, fill bool) bool {
	value, ok := container[key]
	if !ok {
		return false
	}
	parts := identityParts(value, func(v string) { container[key] = v })
	var start strings.Builder
	for _, part := range parts {
		start.WriteString(part.value)
		if start.Len() >= len(identityFill)+len(harnessIdentity) {
			break
		}
	}
	prefix := start.String()
	if prefix == "" {
		return false
	}
	hasFill := strings.HasPrefix(prefix, identityFill)
	offset := 0
	if hasFill {
		offset = len(identityFill)
	}
	changed := false
	if rewrite && strings.HasPrefix(prefix[offset:], harnessIdentity) {
		end := offset + len(harnessIdentity)
		position, inserted := 0, false
		insertedPart, insertedAt := -1, 0
		emptied := []int{}
		for i := range parts {
			original := parts[i].value
			partEnd := position + len(original)
			if partEnd > offset && position < end {
				from := max(0, offset-position)
				to := min(len(original), end-position)
				replacement := ""
				if !inserted {
					replacement, inserted = neutralIdentity, true
					insertedPart, insertedAt = i, from
				}
				parts[i].value = original[:from] + replacement + original[to:]
				if original != "" && parts[i].value == "" {
					emptied = append(emptied, i)
				}
			}
			position = partEnd
			if position >= end {
				break
			}
		}
		// Some APIs reject empty text blocks. Keep formerly nonempty blocks
		// nonempty by moving trailing replacement characters into them.
		if n := len(emptied); n > 0 && n < len(neutralIdentity) {
			cut := insertedAt + len(neutralIdentity) - n
			first := parts[insertedPart].value
			parts[insertedPart].value = first[:cut] + first[cut+n:]
			for i, index := range emptied {
				parts[index].value = neutralIdentity[len(neutralIdentity)-n+i : len(neutralIdentity)-n+i+1]
			}
		}
		changed = true
	}
	if fill && !hasFill {
		parts[0].value = identityFill + parts[0].value
		changed = true
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
