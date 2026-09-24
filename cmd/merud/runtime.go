// This file holds merud's startup checks on the model runtime: the Ollama
// version check and the warm-up call for each model tier.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// minOllama is the oldest Ollama that reports log probabilities, which the
// router needs. See ARCHITECTURE.md, "Model tiers".
const minOllama = "0.12.11"

// checkRuntime asks the engine what it runs on and returns the runtime's
// version. It fails when the runtime can't be reached or is older than
// minOllama, and the message names both versions.
func checkRuntime(ctx context.Context, eng engine.Engine) (string, error) {
	info, err := eng.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("reach Ollama: %w (is it running?)", err)
	}
	ok, err := versionAtLeast(info.RuntimeVersion, minOllama)
	if err != nil {
		return "", fmt.Errorf("read Ollama version: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("found Ollama %s, which is too old; Meru needs %s or later for log probabilities", info.RuntimeVersion, minOllama)
	}
	return info.RuntimeVersion, nil
}

// versionAtLeast reports whether version have is at least want. Both are
// dotted numbers such as "0.12.11", with an optional leading "v". Anything
// after a "-" or "+" (a pre-release or build tag) is ignored, so "0.12.11-rc1"
// counts as 0.12.11. It fails when either string isn't in that form.
func versionAtLeast(have, want string) (bool, error) {
	h, err := parseVersion(have)
	if err != nil {
		return false, err
	}
	w, err := parseVersion(want)
	if err != nil {
		return false, err
	}
	for i := range h {
		if h[i] != w[i] {
			return h[i] > w[i], nil
		}
	}
	return true, nil
}

// parseVersion turns "v1.2.3-rc1" into [1 2 3]. Missing parts count as zero,
// so "0.13" is [0 13 0].
func parseVersion(s string) ([3]int, error) {
	var v [3]int // an array: fixed length 3, all zeros to start
	core := strings.TrimPrefix(s, "v")
	// strings.Cut splits at the first separator and drops the rest.
	core, _, _ = strings.Cut(core, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return v, fmt.Errorf("version %q isn't in the form 1.2.3", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("version %q isn't in the form 1.2.3", s)
		}
		v[i] = n
	}
	return v, nil
}

// warm loads each tier's model into Ollama with a tiny request, so the first
// real question doesn't wait for a model to load from disk. The engine sends
// keep_alive from config with every call, so the models then stay loaded.
//
// When fast and main name the same model, as in the lite profile, it loads
// once. warm fails on the first model that can't load, which usually means it
// hasn't been pulled yet, and says so.
func warm(ctx context.Context, eng engine.Engine, m config.Models, log *slog.Logger) error {
	chat := []string{m.Fast}
	if m.Main != m.Fast {
		chat = append(chat, m.Main)
	}
	hello := []engine.Message{{Role: engine.RoleUser, Content: "hi"}}
	for _, model := range chat {
		start := time.Now()
		log.DebugContext(ctx, "warming", "model", model)
		_, err := eng.Generate(ctx, hello, nil, engine.Options{Model: model, MaxTokens: 1})
		if err != nil {
			return fmt.Errorf("warm %s: %w (try `ollama pull %s`)", model, err, model)
		}
		log.Info("warmed", "model", model, "ms", time.Since(start).Milliseconds())
	}

	start := time.Now()
	log.DebugContext(ctx, "warming", "model", m.Embed)
	if _, err := eng.Embed(ctx, []string{"hi"}); err != nil {
		return fmt.Errorf("warm %s: %w (try `ollama pull %s`)", m.Embed, err, m.Embed)
	}
	log.Info("warmed", "model", m.Embed, "ms", time.Since(start).Milliseconds())
	return nil
}
