package gateway

import (
	"math/rand/v2"
	"strings"
)

const (
	codarIdentity   = "You are an AI agent running on the Codar runtime."
	harnessIdentity = "You are an AI agent powered by DeepSeek Harness."
	neutralIdentity = "You are an AI coding agent."
	legacyFill      = ".\u200b"
	legacyFillX     = legacyFill + "X"
)

func randomIdentityFill() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 8+rand.IntN(9))
	for i := range buf {
		buf[i] = letters[rand.IntN(len(letters))]
	}
	return string(buf)
}

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

// transformIdentity replaces a leading Harness identity and adds the Codar
// identity line and optional fill, preserving text block metadata.
func transformIdentity(container M, key string, rewrite, fill bool) bool {
	value, ok := container[key]
	if !ok {
		return false
	}
	parts := identityParts(value, func(v string) { container[key] = v })
	if len(parts) == 0 {
		return false
	}
	nonempty := make([]bool, len(parts))
	for i, part := range parts {
		nonempty[i] = part.value != ""
	}
	var start strings.Builder
	for _, part := range parts {
		start.WriteString(part.value)
		if start.Len() >= len(legacyFillX)+len(codarIdentity)+1+len(harnessIdentity) {
			break
		}
	}
	prefix := start.String()
	offset := 0
	if strings.HasPrefix(prefix, legacyFillX) {
		offset = len(legacyFillX)
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
	identityOffset := offset
	if strings.HasPrefix(prefix[offset:], codarIdentity+"\n") {
		identityOffset += len(codarIdentity) + 1
	}
	if rewrite && strings.HasPrefix(prefix[identityOffset:], harnessIdentity) {
		position, inserted := 0, false
		end := identityOffset + len(harnessIdentity)
		for i := range parts {
			original := parts[i].value
			partEnd := position + len(original)
			if partEnd > identityOffset && position < end {
				from, to := max(0, identityOffset-position), min(len(original), end-position)
				replacement := ""
				if !inserted {
					replacement, inserted = neutralIdentity, true
				}
				parts[i].value = original[:from] + replacement + original[to:]
			}
			position = partEnd
			if position >= end {
				break
			}
		}
		changed = true
	}
	if rewrite && !strings.HasPrefix(prefix[offset:], codarIdentity+"\n") {
		insert(offset, codarIdentity+"\n")
		changed = true
	}
	if fill {
		// Replace any old fill, including when it spans text blocks.
		remaining := offset
		for i := range parts {
			n := min(remaining, len(parts[i].value))
			parts[i].value = parts[i].value[n:]
			remaining -= n
			if remaining == 0 {
				break
			}
		}
		insert(0, randomIdentityFill())
		changed = true
	}
	if changed {
		// Replacing an identity spanning blocks may empty a text block. Move
		// trailing characters forward without changing the concatenated prompt.
		for i := range parts {
			if !nonempty[i] || parts[i].value != "" {
				continue
			}
			for donor := i - 1; donor >= 0; donor-- {
				if len([]rune(parts[donor].value)) <= 1 {
					continue
				}
				for j := donor; j < i; j++ {
					runes := []rune(parts[j].value)
					parts[j].value = string(runes[:len(runes)-1])
					parts[j+1].value = string(runes[len(runes)-1]) + parts[j+1].value
				}
				break
			}
		}
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
