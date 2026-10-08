package claude

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestRelease1StreamErrorFramePassesUpstreamObjectUnchanged(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	upstream := `{"type":"billing_error","code":"payment_required","message":"Your plan needs renewal.","future_field":{"k":1}}`
	for _, status := range []int{http.StatusPaymentRequired, http.StatusForbidden, http.StatusBadGateway} {
		msg := &interfaces.ErrorMessage{StatusCode: status, Error: errors.New(`{"error":` + upstream + `,"sequence_number":7}`)}
		frame := string(handler.claudeStreamErrorFrame(msg))
		want := "event: error\ndata: {\"type\":\"error\",\"error\":" + upstream + "}\n\n"
		if frame != want {
			t.Fatalf("status %d frame = %q, want %q", status, frame, want)
		}
	}
}

func TestRelease1StreamErrorFrameCompactsMultilineObject(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{StatusCode: http.StatusPaymentRequired, Error: errors.New("{\"error\":{\n  \"code\": \"payment_required\",\n  \"message\": \"renew\"\n}}")}
	frame := string(handler.claudeStreamErrorFrame(msg))
	data := strings.TrimSuffix(strings.TrimPrefix(frame, "event: error\ndata: "), "\n\n")
	if strings.Contains(data, "\n") || gjson.Get(data, "error.code").String() != "payment_required" || gjson.Get(data, "type").String() != "error" {
		t.Fatalf("frame = %q, want one compact data line with the upstream object", frame)
	}
}

func TestRelease1StreamErrorFrameKeepsStockTextOtherwise(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	overflow := &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_too_large"}}`)}
	if frame := string(handler.claudeStreamErrorFrame(overflow)); !strings.Contains(frame, "prompt is too long") {
		t.Fatalf("overflow frame = %q, want the R1-D prompt-is-too-long text", frame)
	}
	plain := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream stream closed before first payload")}
	frame := string(handler.claudeStreamErrorFrame(plain))
	data := strings.TrimSuffix(strings.TrimPrefix(frame, "event: error\ndata: "), "\n\n")
	if gjson.Get(data, "type").String() != "error" || gjson.Get(data, "error.message").String() == "" {
		t.Fatalf("plain frame = %q, want the stock Claude error envelope", frame)
	}
}
