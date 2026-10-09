package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestRelease1WriteErrorResponseKeepsAnthropicEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := `{"type":"error","error":{"type":"invalid_request_error","message":"bad field","future_error":{"k":1}},"request_id":"req_fixture"}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&ClaudeCodeAPIHandler{}).WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(upstream)})
	if recorder.Code != http.StatusBadRequest || recorder.Body.String() != upstream {
		t.Fatalf("got %d %s, want 400 and the upstream envelope unchanged", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	(&ClaudeCodeAPIHandler{}).WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New("local refusal text")})
	if recorder.Body.String() == "local refusal text" || recorder.Code != http.StatusBadRequest {
		t.Fatalf("plain error body = %s, want the stock Claude envelope", recorder.Body.String())
	}
}

func TestWriteErrorResponseKeepsNonCompactAnthropicEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := "{\n  \"type\": \"error\",\n  \"error\": {\"type\": \"invalid_request_error\", \"message\": \"bad field\"},\n  \"request_id\": \"req_fixture\"\n}"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&ClaudeCodeAPIHandler{}).WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(upstream)})
	if recorder.Code != http.StatusBadRequest || recorder.Body.String() != upstream {
		t.Fatalf("got %d %q, want 400 and the upstream envelope byte for byte", recorder.Code, recorder.Body.String())
	}
}
