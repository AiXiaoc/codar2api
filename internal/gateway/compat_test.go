package gateway

import "testing"

func TestIdentityOnlyChangesExistingInstructions(t *testing.T) {
	payload := M{"messages": []any{M{"role": "user", "content": "hello"}}, "tools": []any{M{"description": "unchanged"}}}
	if transformIdentityPrompts("/v1/chat/completions", payload, true, true) {
		t.Fatal("request without system or developer instructions must remain unchanged")
	}
	if transformIdentityPrompts("/unknown", M{"system": "rules"}, true, true) {
		t.Fatal("unknown API must remain unchanged")
	}
}
