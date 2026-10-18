// This file holds `meru transcribe file.wav`: it sends a recording to
// merud, which turns it into text with the whisper connector, and prints
// the words. It is the way to try speech to text without Meru.app, and a
// script's way to use it. merud does the work; this side reads the file
// and prints.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aarora79/meru/internal/rpc"
)

// maxRecording is the largest file transcribe sends: a minute of 16 kHz
// mono 16-bit audio is 1.9 MB, and a stereo or 48 kHz minute is more but
// still well under the socket's line once base64 grows it. merud checks
// the length itself; this cap only stops a wrong file, such as a film,
// from being read into memory.
const maxRecording = 3 << 20

// transcribeCmd runs `meru transcribe <file.wav>`. It prints the words,
// or a line saying the recording held none. It fails when the file can't
// be read or is too large, when merud can't be reached, and with merud's
// reason when the connector is off or the file isn't a WAV recording.
func transcribeCmd(ctx context.Context, socket string, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: meru transcribe <file.wav>")
	}
	info, err := os.Stat(args[0])
	if err != nil {
		return err
	}
	if info.Size() > maxRecording {
		return fmt.Errorf("%s is %d MB; a minute of speech as a 16 kHz mono WAV file is 2 MB", args[0], info.Size()>>20)
	}
	wav, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpTranscribe, Audio: &rpc.Audio{WAV: wav}}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventTranscript:
			if ev.Text == "" {
				fmt.Fprintln(stdout, "(Meru heard no words.)")
				continue
			}
			fmt.Fprintln(stdout, ev.Text)
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return nil
}
