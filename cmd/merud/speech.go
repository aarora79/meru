// This file answers the transcribe op: it checks the recording, asks the
// whisper connector for its server, which starts it when it isn't
// running, and sends back the words whisper-server heard. merud keeps
// neither the audio nor the text, and logs only the length, the time and
// the outcome. See ARCHITECTURE.md, "Speech to text".

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/speech"
)

// speechTimeout caps one transcription once the server runs. A minute of
// audio takes under a second with base.en and a few with large-v3-turbo
// on Apple silicon; two minutes leaves room for a slow Mac.
const speechTimeout = 2 * time.Minute

// speechClient is the HTTP client for whisper-server. It follows no
// redirect, since the server is on loopback and has none to give, and a
// redirect could send the audio elsewhere.
var speechClient = &http.Client{
	Timeout: speechTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("speech to text: the server tried to redirect")
	},
}

// handleTranscribe answers OpTranscribe with one "transcript" event. It
// fails with no recording, with a file that isn't WAV or runs past
// speech.MaxSeconds, with the connector's sentence when it is off,
// installing or failed, and when whisper-server fails.
func (s *toolService) handleTranscribe(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	start := time.Now()
	model := s.conns.speech.Value("model")
	outcome := "error"
	var seconds float64
	// The deferred function runs when handleTranscribe returns, whatever
	// the path, so every request is measured and logged once.
	defer func() {
		obs.RecordSpeech(ctx, model, outcome, time.Since(start))
		s.log.InfoContext(ctx, "transcribe", "model", model, "seconds", fmt.Sprintf("%.1f", seconds),
			"ms", time.Since(start).Milliseconds(), "outcome", outcome)
	}()

	if req.Audio == nil || len(req.Audio.WAV) == 0 {
		return errors.New("transcribe needs a recording")
	}
	seconds, err := speech.CheckWAV(req.Audio.WAV)
	if errors.Is(err, speech.ErrTooLong) {
		outcome = "too_long"
		return fmt.Errorf("the recording runs %.0f seconds; Meru takes %d at most", seconds, speech.MaxSeconds)
	}
	if err != nil {
		return err
	}
	base, done, err := s.conns.speech.Use(ctx)
	if err != nil {
		return err
	}
	// done tells the connector this request is over, so its idle timer
	// can start.
	defer done()
	text, err := speech.Transcribe(ctx, speechClient, base, req.Audio.WAV)
	switch {
	case ctx.Err() != nil:
		outcome = "cancelled"
		return ctx.Err()
	case err != nil:
		return err
	case text == "":
		outcome = "empty"
	default:
		outcome = "ok"
	}
	return emit(rpc.Event{Type: rpc.EventTranscript, Text: text})
}
