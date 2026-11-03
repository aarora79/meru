// This file serves POST /v1/chat/completions, the OpenAI-style API that
// the real Ollama also serves, so tests can point an [[endpoints]] entry
// at the fake. It answers from the same queue of
// scripted replies as /api/chat, and streams server-sent events, one
// "data: {json}" line per piece and "data: [DONE]" at the end.

package fakeollama

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openAIRequest is the part of a /v1/chat/completions body the fake reads.
// A message's content is a string or a list of parts, so it stays raw.
type openAIRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	MaxTokens int `json:"max_tokens"`
}

// serveOpenAIChat answers POST /v1/chat/completions with the next scripted
// reply for the model: text in pieces, then tool calls, then the finish
// reason and the token counts. A scripted Status fails the call with an
// OpenAI-style error body.
func (f *Fake) serveOpenAIChat(w http.ResponseWriter, r *http.Request, body []byte, id int) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if !f.accepts(req.Model) {
		writeOpenAIError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
		return
	}
	rep := f.nextReply(req.Model)
	if rep.Status != 0 {
		writeOpenAIError(w, rep.Status, rep.Error)
		return
	}
	f.touch(req.Model, nil)
	words := 0
	for _, m := range req.Messages {
		words += len(strings.Fields(string(m.Content)))
	}
	chunks := rep.pieces()
	reason := "stop"
	if len(rep.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	if req.MaxTokens > 0 && len(chunks) > req.MaxTokens {
		chunks, reason = chunks[:req.MaxTokens], "length"
	}
	usage := map[string]int{"prompt_tokens": words, "completion_tokens": len(chunks)}

	if !sleep(r.Context(), rep.Delay) {
		f.markCancelled(id)
		return
	}
	if !req.Stream {
		msg := map[string]any{"role": "assistant", "content": strings.Join(chunks, "")}
		if calls := openAIToolCalls(rep.ToolCalls); len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": reason}},
			"usage":   usage,
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	delta := func(d map[string]any, finish any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": finish}}}
	}
	for i, chunk := range chunks {
		if i > 0 && !sleep(r.Context(), rep.ChunkDelay) {
			f.markCancelled(id)
			return
		}
		if !send(delta(map[string]any{"content": chunk}, nil)) {
			f.markCancelled(id)
			return
		}
	}
	if calls := openAIToolCalls(rep.ToolCalls); len(calls) > 0 {
		send(delta(map[string]any{"tool_calls": calls}, nil))
	}
	send(delta(map[string]any{}, reason))
	send(map[string]any{"choices": []any{}, "usage": usage})
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
}

// openAIToolCalls writes scripted tool calls the way the API sends them:
// each with an index, an ID, and its arguments as a JSON string.
func openAIToolCalls(calls []ToolCall) []any {
	var out []any
	for i, tc := range calls {
		args := tc.Arguments
		if args == nil {
			args = map[string]any{}
		}
		b, _ := json.Marshal(args)
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out = append(out, map[string]any{"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": tc.Name, "arguments": string(b)}})
	}
	return out
}

// writeOpenAIError writes an error in the API's shape:
// {"error": {"message": "..."}}.
func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg}})
}
