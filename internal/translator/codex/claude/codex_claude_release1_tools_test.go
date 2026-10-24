package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRelease1CodexToolsCarryOnlyFunctionFields(t *testing.T) {
	input := []byte(`{"model":"gpt-6.1-sol","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"type":"web_fetch_20250910","name":"web_fetch","max_uses":5,"allowed_domains":["example.com"],"citations":{"enabled":true},"max_content_tokens":1000},` +
		`{"name":"Read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"cache_control":{"type":"ephemeral"},"eager_input_streaming":true},` +
		`{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`)
	out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, false)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex() error = %v, want nil", err)
	}
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3: %s", len(tools), gjson.GetBytes(out, "tools").Raw)
	}
	allowed := map[string]bool{"type": true, "name": true, "description": true, "parameters": true, "strict": true}
	for i, tool := range tools[:2] {
		tool.ForEach(func(key, _ gjson.Result) bool {
			if !allowed[key.String()] {
				t.Errorf("tool %d keeps non-function field %q: %s", i, key.String(), tool.Raw)
			}
			return true
		})
		if tool.Get("type").String() != "function" {
			t.Errorf("tool %d type = %q, want function", i, tool.Get("type").String())
		}
	}
	if tools[2].Get("type").String() != "web_search" || tools[2].Get("max_uses").Exists() {
		t.Fatalf("web search tool = %s, want stock web_search without max_uses", tools[2].Raw)
	}
}

// Key names that an sjson path would read as path syntax ("x.y", "a:b", an
// escaped dot, and "parameters.properties") must not survive, and must not
// remove nested content from the allowed parameters field.
func TestRelease1CodexToolsCarryOnlyFunctionFieldsWithPathSyntaxKeys(t *testing.T) {
	input := []byte(`{"model":"gpt-6.1-sol","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"name":"Grep","description":"search","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}}},"x.y":1,"a:b":2,"c\\.d":3,"parameters.properties":{"stray":true},"max_uses":4}]}`)
	out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, false)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex() error = %v, want nil", err)
	}
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1: %s", len(tools), gjson.GetBytes(out, "tools").Raw)
	}
	allowed := map[string]bool{"type": true, "name": true, "description": true, "parameters": true, "strict": true}
	tools[0].ForEach(func(key, _ gjson.Result) bool {
		if !allowed[key.String()] {
			t.Errorf("tool keeps non-function field %q: %s", key.String(), tools[0].Raw)
		}
		return true
	})
	if got := tools[0].Get("parameters.properties.pattern.type").String(); got != "string" {
		t.Fatalf("parameters lost nested content: %s", tools[0].Get("parameters").Raw)
	}
	if tools[0].Get("parameters.properties.stray").Exists() {
		t.Fatalf("parameters gained a stray field: %s", tools[0].Get("parameters").Raw)
	}
}
