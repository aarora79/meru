// This file holds the fake's Ollama endpoints: version, ps, chat, generate
// and embed. Chat and generate share one reply writer, because the two differ
// only in where the text goes ("message.content" or "response").

package fakeollama

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"
)

// serveVersion answers GET /api/version.
func (f *Fake) serveVersion(w http.ResponseWriter, _ *http.Request, _ []byte, _ int) {
	writeJSON(w, http.StatusOK, map[string]string{"version": f.cfg.Version})
}

// servePS answers GET /api/ps with the models currently loaded.
func (f *Fake) servePS(w http.ResponseWriter, _ *http.Request, _ []byte, _ int) {
	f.mu.Lock()
	names := append([]string(nil), f.loaded...)
	f.mu.Unlock()

	models := []psModel{} // an empty list, not null, when nothing is loaded
	expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	for _, name := range names {
		models = append(models, psModel{
			Name:          name,
			Model:         name,
			Size:          1 << 30,
			Digest:        digest(name),
			Details:       psDetails{Format: "gguf", Family: "fake", ParameterSize: "1B", QuantizationLevel: "Q4_K_M"},
			ExpiresAt:     expires,
			SizeVRAM:      1 << 30,
			ContextLength: 8192,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// digest makes up a stable digest for a model name, standing in for the
// sha256 of the model file that Ollama reports.
func digest(name string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name)) // a hash's Write never fails, so the results are dropped
	return fmt.Sprintf("%016x", h.Sum64())
}

// serveChat answers POST /api/chat.
func (f *Fake) serveChat(w http.ResponseWriter, r *http.Request, body []byte, id int) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	var words int
	for _, m := range req.Messages {
		words += len(strings.Fields(m.Content))
	}
	c := call{
		model:       req.Model,
		stream:      req.Stream == nil || *req.Stream,
		logProbs:    req.LogProbs,
		topLogProbs: req.TopLogProbs,
		promptWords: words,
		keepAlive:   req.KeepAlive,
		chat:        true,
	}
	f.reply(w, r, id, c)
}

// serveGenerate answers POST /api/generate.
func (f *Fake) serveGenerate(w http.ResponseWriter, r *http.Request, body []byte, id int) {
	var req generateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	c := call{
		model:       req.Model,
		stream:      req.Stream == nil || *req.Stream,
		logProbs:    req.LogProbs,
		topLogProbs: req.TopLogProbs,
		promptWords: len(strings.Fields(req.System)) + len(strings.Fields(req.Prompt)),
		keepAlive:   req.KeepAlive,
	}
	f.reply(w, r, id, c)
}

// call is what reply needs to know about one chat or generate request.
type call struct {
	model       string
	stream      bool
	logProbs    bool
	topLogProbs int
	promptWords int
	keepAlive   json.RawMessage
	chat        bool // true for /api/chat, false for /api/generate
}

// reply writes the answer to one chat or generate call: the next scripted
// Reply for the model, streamed as NDJSON (one JSON object per line) or sent
// as one object.
func (f *Fake) reply(w http.ResponseWriter, r *http.Request, id int, c call) {
	if c.model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if !f.accepts(c.model) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found, try pulling it first", c.model))
		return
	}
	rep := f.nextReply(c.model)
	if rep.Status != 0 {
		writeError(w, rep.Status, rep.Error)
		return
	}
	if rep.StreamError != "" && !c.stream {
		writeError(w, http.StatusInternalServerError, rep.StreamError)
		return
	}
	f.touch(c.model, c.keepAlive)

	chunks := rep.pieces()
	logProbs := rep.logProbsFor(chunks, c)
	final := rep.counters(c.promptWords, len(chunks))
	doneReason := rep.DoneReason
	if doneReason == "" {
		doneReason = "stop"
	}
	if !sleep(r.Context(), rep.Delay) {
		f.markCancelled(id)
		return
	}

	if !c.stream {
		obj := f.object(c, strings.Join(chunks, ""), rep.ToolCalls, logProbs)
		setDone(obj, doneReason, final)
		writeJSON(w, http.StatusOK, obj)
		return
	}

	// Streaming: one line per chunk, then a last line with done set. Flush
	// after each line so the client sees tokens as they come, which is what
	// the time-to-first-token metric measures.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	// http.NewResponseController reaches the Flush method that the
	// ResponseWriter interface itself doesn't show.
	rc := http.NewResponseController(w)
	send := func(v any) bool {
		if err := enc.Encode(v); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	for i, chunk := range chunks {
		if rep.StreamError != "" && i == rep.FailAfter {
			send(errorBody{Error: rep.StreamError})
			return
		}
		if i > 0 && !sleep(r.Context(), rep.ChunkDelay) {
			f.markCancelled(id)
			return
		}
		var lp []wireLogProb
		if i < len(logProbs) {
			lp = logProbs[i : i+1]
		}
		if !send(f.object(c, chunk, nil, lp)) {
			f.markCancelled(id)
			return
		}
	}
	if rep.StreamError != "" {
		send(errorBody{Error: rep.StreamError})
		return
	}
	if len(rep.ToolCalls) > 0 && c.chat {
		// Ollama sends tool calls in their own chunk before the last one.
		if !send(f.object(c, "", rep.ToolCalls, nil)) {
			f.markCancelled(id)
			return
		}
	}
	last := f.object(c, "", nil, nil)
	setDone(last, doneReason, final)
	if !send(last) {
		f.markCancelled(id)
	}
}

// object builds one chat or generate response object holding text, and tool
// calls for chat. It returns a pointer so setDone can fill in the ending.
func (f *Fake) object(c call, text string, calls []ToolCall, lp []wireLogProb) any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if !c.chat {
		return &generateResponse{Model: c.model, CreatedAt: now, Response: text, LogProbs: lp}
	}
	msg := chatMessage{Role: "assistant", Content: text}
	for _, tc := range calls {
		args := tc.Arguments
		if args == nil {
			args = map[string]any{}
		}
		msg.ToolCalls = append(msg.ToolCalls, wireToolCall{ID: tc.ID, Function: wireFunction{Name: tc.Name, Arguments: args}})
	}
	return &chatResponse{Model: c.model, CreatedAt: now, Message: msg, LogProbs: lp}
}

// setDone marks obj, built by object, as the last one, with the stop reason
// and the usage counters.
func setDone(obj any, reason string, cs counters) {
	// A type switch picks a branch by the dynamic type stored in obj.
	switch o := obj.(type) {
	case *chatResponse:
		o.Done, o.DoneReason, o.counters = true, reason, cs
	case *generateResponse:
		o.Done, o.DoneReason, o.counters = true, reason, cs
	}
}

// pieces returns the chunks the reply streams: Chunks when set, otherwise
// Text split after each space, otherwise the tokens from LogProbs. A reply
// with only tool calls has no text chunks.
func (rep Reply) pieces() []string {
	switch {
	case len(rep.Chunks) > 0:
		return rep.Chunks
	case rep.Text != "":
		return strings.SplitAfter(rep.Text, " ")
	case len(rep.LogProbs) > 0:
		out := make([]string, len(rep.LogProbs))
		for i, lp := range rep.LogProbs {
			out[i] = lp.Token
		}
		return out
	}
	return nil
}

// logProbsFor returns the log probabilities to send, one per chunk, or nil
// when the request didn't ask for them. Scripted values win; otherwise each
// chunk gets a made-up entry with c.topLogProbs alternatives (at most 20, as
// in Ollama).
func (rep Reply) logProbsFor(chunks []string, c call) []wireLogProb {
	if !c.logProbs {
		return nil
	}
	top := min(c.topLogProbs, 20)
	if len(rep.LogProbs) > 0 {
		out := make([]wireLogProb, len(rep.LogProbs))
		for i, lp := range rep.LogProbs {
			out[i] = wireLogProb{Token: lp.Token, LogProb: lp.LogProb, Bytes: byteList(lp.Token)}
			for j, alt := range lp.Top {
				if j == top {
					break
				}
				out[i].TopLogProbs = append(out[i].TopLogProbs, wireTokenLog{Token: alt.Token, LogProb: alt.LogProb, Bytes: byteList(alt.Token)})
			}
		}
		return out
	}
	out := make([]wireLogProb, len(chunks))
	for i, chunk := range chunks {
		// log(0.9) ≈ -0.105: the model was fairly sure of each token.
		out[i] = wireLogProb{Token: chunk, LogProb: math.Log(0.9), Bytes: byteList(chunk)}
		for j := range top {
			tok, lp := chunk, math.Log(0.9)
			if j > 0 {
				tok, lp = fmt.Sprintf("alt%d", j), math.Log(0.1/float64(j+1))
			}
			out[i].TopLogProbs = append(out[i].TopLogProbs, wireTokenLog{Token: tok, LogProb: lp, Bytes: byteList(tok)})
		}
	}
	return out
}

// counters works out the usage numbers for the last object. The durations
// are made up but fixed: 1ms per prompt token and 10ms per output token, so
// tests can check exact values.
func (rep Reply) counters(promptWords, chunks int) counters {
	prompt, output := rep.PromptTokens, rep.OutputTokens
	if prompt == 0 {
		prompt = promptWords
	}
	if output == 0 {
		output = chunks
	}
	promptDur := int64(prompt) * int64(time.Millisecond)
	evalDur := int64(output) * int64(10*time.Millisecond)
	load := int64(rep.LoadDuration)
	return counters{
		TotalDuration:      load + promptDur + evalDur,
		LoadDuration:       load,
		PromptEvalCount:    prompt,
		PromptEvalDuration: promptDur,
		EvalCount:          output,
		EvalDuration:       evalDur,
	}
}

// byteList returns the UTF-8 bytes of s as ints, the form Ollama uses in its
// "bytes" field.
func byteList(s string) []int {
	out := make([]int, len(s))
	for i := range len(s) {
		out[i] = int(s[i])
	}
	return out
}

// serveEmbed answers POST /api/embed with one vector per input text. The
// vectors come from a hash of the text, so the same text always gets the
// same vector and different texts get different ones.
func (f *Fake) serveEmbed(w http.ResponseWriter, _ *http.Request, body []byte, _ int) {
	var req embedRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if !f.accepts(req.Model) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found, try pulling it first", req.Model))
		return
	}
	texts, err := embedInputs(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.touch(req.Model, req.KeepAlive)

	dims := f.cfg.EmbedDims
	if req.Dimensions > 0 {
		dims = req.Dimensions
	}
	resp := embedResponse{Model: req.Model, Embeddings: [][]float32{}}
	for _, text := range texts {
		resp.Embeddings = append(resp.Embeddings, vectorFor(text, dims))
		resp.PromptEvalCount += len(strings.Fields(text))
	}
	resp.TotalDuration = int64(resp.PromptEvalCount) * int64(time.Millisecond)
	writeJSON(w, http.StatusOK, resp)
}

// embedInputs decodes the "input" field, which may be one string or a list.
func embedInputs(raw json.RawMessage) ([]string, error) {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("input must be a string or a list of strings")
	}
	return many, nil
}

// vectorFor returns a unit-length vector of dims numbers made from a hash of
// text. Unit length matches what embedding models return, so cosine
// similarity in tests behaves as it would with a real model.
func vectorFor(text string, dims int) []float32 {
	v := make([]float32, dims)
	var norm float64
	for i := range v {
		h := fnv.New64a()
		fmt.Fprintf(h, "%d:%s", i, text)
		// Map the 64-bit hash to a number between -1 and 1.
		x := float64(h.Sum64())/math.MaxUint64*2 - 1
		v[i] = float32(x)
		norm += x * x
	}
	if norm == 0 {
		return v
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}
