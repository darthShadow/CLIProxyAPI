package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func release1ConvertStream(t *testing.T, events []string) string {
	t.Helper()
	var param any
	var out strings.Builder
	for _, event := range events {
		for _, chunk := range ConvertCodexResponseToClaude(context.Background(), "gpt-6.1-sol", []byte(`{"model":"gpt-6.1-sol"}`), nil, []byte("data: "+event), &param) {
			out.Write(chunk)
		}
	}
	return out.String()
}

// release1TextDeltas returns every text_delta text and the final stop_reason.
func release1TextDeltas(stream string) ([]string, string) {
	var texts []string
	stopReason := ""
	for _, line := range strings.Split(stream, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if payload.Get("delta.type").String() == "text_delta" {
			texts = append(texts, payload.Get("delta.text").String())
		}
		if reason := payload.Get("delta.stop_reason"); reason.Exists() && reason.String() != "" {
			stopReason = reason.String()
		}
	}
	return texts, stopReason
}

const (
	release1Refusal       = "I can't help with that."
	release1RefusalItem   = `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"I can't help with that."}]}`
	release1TerminalText  = "Recovered from the terminal event."
	release1TerminalItem  = `{"id":"msg_2","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Recovered from the terminal event.","annotations":[]}]}`
	release1CreatedEvent  = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol"}}`
	release1UsageFragment = `"usage":{"input_tokens":10,"output_tokens":5}`
)

func TestRelease1CodexStreamShowsRefusalTextWithEndTurn(t *testing.T) {
	cases := []struct {
		name   string
		events []string
	}{
		{name: "refusal deltas", events: []string{
			release1CreatedEvent,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}`,
			`{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"I can't help with that."}`,
			`{"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"I can't help with that."}`,
			`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":"I can't help with that."}}`,
			`{"type":"response.output_item.done","output_index":0,"item":` + release1RefusalItem + `}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1RefusalItem + `],` + release1UsageFragment + `}}`,
		}},
		{name: "refusal only in output_item.done", events: []string{
			release1CreatedEvent,
			`{"type":"response.output_item.done","output_index":0,"item":` + release1RefusalItem + `}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1RefusalItem + `],` + release1UsageFragment + `}}`,
		}},
		{name: "refusal only in the terminal event", events: []string{
			release1CreatedEvent,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1RefusalItem + `],` + release1UsageFragment + `}}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			texts, stopReason := release1TextDeltas(release1ConvertStream(t, tc.events))
			if len(texts) != 1 || texts[0] != release1Refusal {
				t.Fatalf("text deltas = %q, want exactly [%q]", texts, release1Refusal)
			}
			if stopReason != "end_turn" {
				t.Fatalf("stop_reason = %q, want end_turn", stopReason)
			}
		})
	}
}

func TestRelease1CodexStreamRecoversTerminalOnlyText(t *testing.T) {
	cases := []struct {
		name      string
		events    []string
		wantTexts []string
	}{
		{name: "text only in the terminal event", events: []string{
			release1CreatedEvent,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1TerminalItem + `],` + release1UsageFragment + `}}`,
		}, wantTexts: []string{release1TerminalText}},
		{name: "streamed text is not repeated", events: []string{
			release1CreatedEvent,
			`{"type":"response.content_part.added","item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","item_id":"msg_2","output_index":0,"content_index":0,"delta":"Recovered from the terminal event."}`,
			`{"type":"response.content_part.done","item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Recovered from the terminal event."}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1TerminalItem + `],` + release1UsageFragment + `}}`,
		}, wantTexts: []string{release1TerminalText}},
		{name: "streamed item does not hide a terminal-only item", events: []string{
			release1CreatedEvent,
			`{"type":"response.content_part.added","item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","item_id":"msg_2","output_index":0,"content_index":0,"delta":"Recovered from the terminal event."}`,
			`{"type":"response.content_part.done","item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Recovered from the terminal event."}}`,
			`{"type":"response.output_item.done","output_index":0,"item":` + release1TerminalItem + `}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + release1TerminalItem + `,` + `{"id":"msg_3","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Second item only in the terminal event.","annotations":[]}]}` + `],` + release1UsageFragment + `}}`,
		}, wantTexts: []string{release1TerminalText, "Second item only in the terminal event."}},
		{name: "streamed part does not hide a terminal-only part of the same item", events: []string{
			release1CreatedEvent,
			`{"type":"response.content_part.added","item_id":"msg_4","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","item_id":"msg_4","output_index":0,"content_index":0,"delta":"Part zero streamed."}`,
			`{"type":"response.content_part.done","item_id":"msg_4","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Part zero streamed."}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + `{"id":"msg_4","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Part zero streamed.","annotations":[]},{"type":"output_text","text":"Part one only in the terminal event.","annotations":[]}]}` + `],` + release1UsageFragment + `}}`,
		}, wantTexts: []string{"Part zero streamed.", "Part one only in the terminal event."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			texts, stopReason := release1TextDeltas(release1ConvertStream(t, tc.events))
			if strings.Join(texts, "|") != strings.Join(tc.wantTexts, "|") {
				t.Fatalf("text deltas = %q, want %q", texts, tc.wantTexts)
			}
			if stopReason != "end_turn" {
				t.Fatalf("stop_reason = %q, want end_turn", stopReason)
			}
		})
	}
}

func TestRelease1CodexNonStreamShowsRefusalText(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","output":[` + release1RefusalItem + `],` + release1UsageFragment + `}}`)
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "gpt-6.1-sol", []byte(`{"model":"gpt-6.1-sol"}`), nil, raw, nil)
	if got := gjson.GetBytes(out, "content.0.text").String(); got != release1Refusal {
		t.Fatalf("content[0].text = %q, want %q: %s", got, release1Refusal, out)
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", got)
	}
}
