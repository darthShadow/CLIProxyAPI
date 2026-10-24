package executor

import (
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestRelease1CodexTokenizerUsesO200kForGPT6(t *testing.T) {
	o200k, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("o200k: %v", err)
	}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol"} {
		enc, errEnc := tokenizerForCodexModel(model)
		if errEnc != nil {
			t.Fatalf("%s: %v", model, errEnc)
		}
		if enc.GetName() != o200k.GetName() {
			t.Fatalf("%s codec = %s, want %s", model, enc.GetName(), o200k.GetName())
		}
	}
}

func TestRelease1CodexCountAddsFixedImageEstimate(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("o200k: %v", err)
	}
	textOnly := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"}]}]}`)
	withImages := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_image","image_url":"data:image/png;base64,BBBB"}]}]}`)
	imagesOnly := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`)
	base, errBase := countCodexInputTokens(enc, textOnly)
	if errBase != nil {
		t.Fatalf("text only: %v", errBase)
	}
	got, errGot := countCodexInputTokens(enc, withImages)
	if errGot != nil {
		t.Fatalf("with images: %v", errGot)
	}
	if got != base+2*765 {
		t.Fatalf("count with two images = %d, want %d", got, base+2*765)
	}
	only, errOnly := countCodexInputTokens(enc, imagesOnly)
	if errOnly != nil || only != 765 {
		t.Fatalf("image only = %d, %v; want 765", only, errOnly)
	}
}

// Images inside a function_call_output array (the translator's form for a tool
// result that returns an image) count the same as images in message content:
// the fixed estimate, never the data URL as text.
func TestRelease1CodexCountAddsFixedImageEstimateForToolOutput(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("o200k: %v", err)
	}
	largeImage := "data:image/png;base64," + strings.Repeat("QUJD", 4000)
	message := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"` + largeImage + `"}]}]}`)
	toolOutput := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"` + largeImage + `"}]}]}`)
	imageOnlyOutput := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"` + largeImage + `"}]}]}`)
	stringOutput := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":"describe"}]}`)
	inMessage, errMessage := countCodexInputTokens(enc, message)
	if errMessage != nil {
		t.Fatalf("message: %v", errMessage)
	}
	inTool, errTool := countCodexInputTokens(enc, toolOutput)
	if errTool != nil {
		t.Fatalf("tool output: %v", errTool)
	}
	if inTool != inMessage {
		t.Fatalf("tool output count = %d, want %d (same text and image as the message)", inTool, inMessage)
	}
	only, errOnly := countCodexInputTokens(enc, imageOnlyOutput)
	if errOnly != nil || only != 765 {
		t.Fatalf("image-only tool output = %d, %v; want 765", only, errOnly)
	}
	text, errText := countCodexInputTokens(enc, stringOutput)
	if errText != nil || text < 1 || text >= 765 {
		t.Fatalf("string tool output = %d, %v; want the text count only", text, errText)
	}
}
