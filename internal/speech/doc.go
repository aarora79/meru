// Package speech turns a recording into text: it checks that the bytes
// are a WAV file of at most MaxSeconds, posts it to whisper.cpp's
// whisper-server, and cleans what comes back. merud's transcribe op calls
// it with the base URL the whisper connector's Service gives
// (internal/connectors). See ARCHITECTURE.md, "Speech to text".
//
// What it chooses not to do: it starts no program and knows no model
// names, since the connector owns both; it never writes the audio to
// disk, and never logs the words. The answer model never sees the audio:
// the text goes back to the client, which shows it to the user before
// anything is sent.
package speech
