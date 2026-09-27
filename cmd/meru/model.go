// This file holds `meru model`: the model sets table, the switch to a set
// and the save that makes it the default. It sends the same requests as
// the chat's /model (rpc.OpModels, OpModelUse and OpModelSave) and prints
// the table through tui.ModelTable, so the two views can't drift apart.
// merud does the work; meru only asks and prints.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/tui"
)

// modelNote follows the table: how to switch and how to save.
const modelNote = "meru model use <name> switches until merud stops; meru model save makes the models in use the default."

// modelUsage is the error for words `meru model` doesn't take.
const modelUsage = "usage: meru model | meru model use <name> [--rebuild] | meru model save"

// switchTimeout bounds a switch or a save. merud unloads one model and
// loads another before it answers, and a large model can take a minute to
// load from disk.
const switchTimeout = 3 * time.Minute

// modelCmd runs `meru model` with the words after it: none prints the
// table, "use <name>" switches to a set, with "--rebuild" for a set that
// changes the embed model, and "save" writes the models in use to
// config.toml. It fails when merud can't be reached, refuses, or the
// words are wrong.
func modelCmd(ctx context.Context, socket string, words []string, out io.Writer) error {
	var req rpc.Request
	switch {
	case len(words) == 0:
		req = rpc.Request{Op: rpc.OpModels}
	case words[0] == "save" && len(words) == 1:
		req = rpc.Request{Op: rpc.OpModelSave}
	case words[0] == "use" && len(words) == 2:
		req = rpc.Request{Op: rpc.OpModelUse, ID: words[1]}
	case words[0] == "use" && len(words) == 3 && words[2] == "--rebuild":
		req = rpc.Request{Op: rpc.OpModelUse, ID: words[1], Rebuild: true}
	default:
		return errors.New(modelUsage)
	}
	if req.Op != rpc.OpModels {
		// context.WithTimeout returns a copy of ctx that ends after the
		// timeout; cancel frees its timer when modelCmd returns.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, switchTimeout)
		defer cancel()
	}
	if req.Op == rpc.OpModelUse {
		fmt.Fprintf(out, "Switching to %s: merud unloads the old model, then loads the new one, which can take a minute…\n", req.ID)
	}
	info, err := modelsReply(ctx, socket, req)
	if err != nil {
		return err
	}
	switch req.Op {
	case rpc.OpModelUse:
		fmt.Fprintf(out, "%s answers now with %s.\n", req.ID, info.Main)
	case rpc.OpModelSave:
		fmt.Fprintf(out, "Saved: merud starts with %s.\n", info.Main)
	}
	if info.Warning != "" {
		fmt.Fprintln(out, "note: "+info.Warning)
	}
	if req.Op != rpc.OpModels {
		return nil
	}
	fmt.Fprintln(out, strings.Join(tui.ModelTable(info), "\n"))
	if len(info.Sets) > 0 {
		fmt.Fprintln(out, "\n"+modelNote)
	}
	return nil
}

// modelsReply sends req and returns merud's "models" event. It fails when
// merud can't be reached, answers with an error, or sends no models.
func modelsReply(ctx context.Context, socket string, req rpc.Request) (rpc.ModelsInfo, error) {
	var info *rpc.ModelsInfo
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return rpc.ModelsInfo{}, err
		}
		switch ev.Type {
		case rpc.EventModels:
			info = ev.Models
		case rpc.EventError:
			return rpc.ModelsInfo{}, errors.New(ev.Error)
		}
	}
	if info == nil {
		return rpc.ModelsInfo{}, errors.New("merud sent no models")
	}
	return *info, nil
}
