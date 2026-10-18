// This file holds the Bridge's side of the microphone button: whether
// speech to text can run now, and sending a recording to merud's
// transcribe op. The page records and builds the WAV file itself
// (web/js/voice.js); the Bridge only carries it, and keeps no copy. See
// ARCHITECTURE.md, "Recording in Meru.app".

package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// speechConnector is the ID of the connector behind the microphone, as
// merud's connectors op names it.
const speechConnector = "whisper"

// maxRecording caps the WAV file the page may hand over: a minute of
// 16 kHz mono 16-bit audio is 1.9 MB. merud checks the length itself;
// this cap stops a page bug from sending more than the socket takes.
const maxRecording = 3 << 20

// transcribeTimeout bounds one transcription: the speech server may have
// to start and load its model first, a few seconds, and a minute of audio
// then takes a few more.
const transcribeTimeout = 2 * time.Minute

// SpeechState says whether the microphone can be used now. Ready is true
// when the whisper connector is ok; otherwise Sentence says why not, in
// the connector's words, such as "Speech to text is off. Turn it on in
// Settings, Connections."
type SpeechState struct {
	Ready    bool   `json:"ready"`
	Sentence string `json:"sentence"`
}

// SpeechStatus asks merud for the whisper connector's state, so the page
// can say what is wrong before it records anything. It fails when merud
// can't be reached; a merud without the connector gives a sentence that
// says to update it.
func (b *Bridge) SpeechStatus(ctx context.Context) (SpeechState, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpConnectors}, rpc.EventConnectors)
	if err != nil {
		return SpeechState{}, err
	}
	for _, c := range ev.Connectors {
		if c.ID == speechConnector {
			return SpeechState{Ready: c.State == rpc.ConnectorOK, Sentence: c.Sentence}, nil
		}
	}
	return SpeechState{Sentence: "This merud has no speech to text. Update Meru to use the microphone."}, nil
}

// Transcribe sends the recording, a WAV file the page encoded as base64,
// to merud's transcribe op and returns the words, "" when the recording
// held none. The bytes live in memory for this call only. It fails on a
// recording that isn't base64 or is too large, and with merud's reason,
// such as a connector that is off.
func (b *Bridge) Transcribe(ctx context.Context, wavBase64 string) (string, error) {
	wav, err := base64.StdEncoding.DecodeString(wavBase64)
	switch {
	case err != nil:
		return "", fmt.Errorf("the recording didn't arrive whole: %w", err)
	case len(wav) == 0:
		return "", errors.New("the recording is empty")
	case len(wav) > maxRecording:
		return "", fmt.Errorf("the recording is %d MB, more than a minute of speech", len(wav)>>20)
	}
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpTranscribe, Audio: &rpc.Audio{WAV: wav}}, rpc.EventTranscript)
	if err != nil {
		return "", err
	}
	return ev.Text, nil
}
