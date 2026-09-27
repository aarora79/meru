// This file holds the model sets table and the /model command: the box
// that shows the table, the switch to a set, and the save that makes the
// set in use the default. `meru model` prints the same table through
// ModelTable, so the two views can't drift apart. merud does the work
// (rpc.OpModels, OpModelUse and OpModelSave); this file only asks and lays
// out the answer.
//
// The file is models.go, not model.go: model.go holds the Bubble Tea
// Model, the chat screen's state.

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// modelNote closes the /model box: how to switch and how to save.
const modelNote = "/model <name> to switch · /model save to make the current one the default"

// noSets is what the table says when config.toml has no model sets.
const noSets = "No model sets in config.toml. Add [[models.sets]] entries (config.example.toml shows how), then restart merud."

// switchTimeout bounds a switch or a save. merud unloads one model and
// loads another before it answers, and a model of 38 GB can take a minute
// to load from disk.
const switchTimeout = 3 * time.Minute

// modelsTimeout bounds the plain question of which models merud has.
// merud asks Ollama for three lists before it answers, each capped at
// three seconds, so the chat's usual two seconds is too short.
const modelsTimeout = 10 * time.Second

// ModelTable lays out merud's model sets as the lines of a table, one row
// per set under a header, with an arrow on the set in use:
//
//	  MODEL SET     MAIN                    SIZE   THINK    STATE
//	→ qwen-moe      qwen3.6:35b-a3b-mxfp8   38 GB  off      loaded
//	  gemma-moe     gemma4:26b-mxfp8        28 GB  default  on disk
//	  qwen-dense    qwen3.8:27b-mlx             —  off      not pulled
//
// A set that also names a fast or embed model gets a second line under
// its row that names them. When no set is in use, a last line names the
// model that answers now. With no sets it returns one line that says how
// to add them.
func ModelTable(info rpc.ModelsInfo) []string {
	if len(info.Sets) == 0 {
		return []string{noSets}
	}
	// The name and main columns grow to fit the longest entry.
	nameWidth, mainWidth := len("MODEL SET"), len("MAIN")
	for _, s := range info.Sets {
		nameWidth = max(nameWidth, len([]rune(s.Name)))
		mainWidth = max(mainWidth, len([]rune(orDash(s.Main))))
	}
	// %-*s pads a string on the right to a width taken from the argument
	// before it; %*s pads on the left, so the sizes line up on the right.
	line := func(mark, name, main, size, think, state string) string {
		s := fmt.Sprintf("%s %-*s   %-*s   %6s   %-7s   %s", mark, nameWidth, name, mainWidth, main, size, think, state)
		return strings.TrimRight(s, " ")
	}
	out := []string{line(" ", "MODEL SET", "MAIN", "SIZE", "THINK", "STATE")}
	for _, s := range info.Sets {
		mark := " "
		if s.Active {
			mark = "→"
		}
		think := "default"
		if s.ThinkOff {
			think = "off"
		}
		size := "—"
		if s.Bytes > 0 {
			size = DiskSize(s.Bytes)
		}
		out = append(out, line(mark, s.Name, orDash(s.Main), size, think, setState(s)))
		if also := alsoSets(s); also != "" {
			out = append(out, "  "+strings.Repeat(" ", nameWidth)+"   "+also)
		}
	}
	if info.Active == "" && info.Main != "" {
		out = append(out, "", "No set is in use; "+info.Main+" answers now.")
	}
	return out
}

// orDash returns s, or "—" when it is empty, as for a set that leaves the
// main model as it is.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// setState says where a set's main model is: in memory, on disk, or not
// pulled yet. A set with no main model of its own says nothing.
func setState(s rpc.ModelSet) string {
	switch {
	case s.Main == "":
		return ""
	case s.Loaded:
		return "loaded"
	case s.Pulled:
		return "on disk"
	}
	return "not pulled"
}

// alsoSets names a set's fast and embed models, such as "also fast:
// gemma3:1b", or returns "" when it names neither.
func alsoSets(s rpc.ModelSet) string {
	var parts []string
	if s.Fast != "" {
		parts = append(parts, "fast: "+s.Fast)
	}
	if s.Embed != "" {
		parts = append(parts, "embed: "+s.Embed)
	}
	if len(parts) == 0 {
		return ""
	}
	return "also " + strings.Join(parts, " · ")
}

// DiskSize writes a model's size as `ollama list` does, in units of 1,000:
// "38 GB", "8.1 GB", "274 MB". Model sizes are what people compare with
// the numbers Ollama prints, so this differs on purpose from
// rpc.ShortBytes, which matches `ls -lh` for meru.db.
func DiskSize(n int64) string {
	v := float64(n)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for v >= 999.5 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i > 0 && v < 9.95 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

// modelBox is the open /model box. It opens at once with loading set, and
// the next models reply fills in info or err.
type modelBox struct {
	loading bool
	info    *rpc.ModelsInfo
	err     string // why merud sent no models
}

// The things a models reply can answer, for modelsMsg.action.
const (
	actionList = "list" // /model, the refresh timer and the chat's start
	actionUse  = "use"  // /model <name>
	actionSave = "save" // /model save
)

// modelsMsg reports what merud said to a model op. action says which op,
// name the set a switch asked for, info merud's "models" event, and err
// why none came.
type modelsMsg struct {
	action string
	name   string
	info   *rpc.ModelsInfo
	err    error
}

// modelsCmd returns a command that sends req to merud once and hands back
// its "models" event as a modelsMsg for action. timeout bounds the wait.
func modelsCmd(ask askFunc, req rpc.Request, action string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		msg := modelsMsg{action: action, name: req.ID}
		for ev, err := range ask(ctx, req, nil) {
			if err != nil {
				msg.err = err
				return msg
			}
			switch ev.Type {
			case rpc.EventModels:
				msg.info = ev.Models
			case rpc.EventDone:
				if msg.info == nil {
					msg.err = errors.New("merud sent no models")
				}
				return msg
			case rpc.EventError:
				msg.err = errors.New(ev.Error)
				return msg
			}
		}
		msg.err = errors.New("merud sent no reply")
		return msg
	}
}

// listModels returns the command that asks merud for its models, for the
// header and the /model box.
func listModels(ask askFunc) tea.Cmd {
	return modelsCmd(ask, rpc.Request{Op: rpc.OpModels}, actionList, modelsTimeout)
}

// modelCommand runs /model with its argument: alone it opens the box,
// "save" makes the set in use the default, and a set's name switches to
// it, with "--rebuild" after the name for a set that changes the embed
// model. A switch or a save closes any open box and reports on the
// notice line when merud answers.
//
// A switch waits while a turn runs: merud would unload the model that is
// writing the answer.
func (m Model) modelCommand(arg string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(arg)
	m.input.Reset()
	m.layout()
	switch {
	case len(fields) == 0:
		m.modelBox = &modelBox{loading: true}
		m.input.Blur() // the input takes no text while the box is open
		return m, listModels(m.ask)
	case fields[0] == "save" && len(fields) == 1:
		m.notice = "saving the models in use as the default…"
		return m, modelsCmd(m.ask, rpc.Request{Op: rpc.OpModelSave}, actionSave, switchTimeout)
	}
	name := fields[0]
	rebuild := len(fields) == 2 && fields[1] == "--rebuild"
	if len(fields) > 2 || (len(fields) == 2 && !rebuild) {
		m.notice = "/model takes a set's name, with --rebuild after it for a set that changes the embed model"
		return m, nil
	}
	if m.streaming {
		m.notice = "wait for the answer to finish, or press ctrl+c, before you switch models"
		return m, nil
	}
	m.notice = "switching to " + name + ": unloading the old model, then loading the new one…"
	return m, modelsCmd(m.ask, rpc.Request{Op: rpc.OpModelUse, ID: name, Rebuild: rebuild}, actionUse, switchTimeout)
}

// applyModels takes in one models reply. Any reply with models updates
// what the header shows. A reply to a switch or a save says on the notice
// line what happened, with merud's warning when it sent one. A waiting
// /model box takes the reply to the list.
func (m *Model) applyModels(msg modelsMsg) {
	if msg.info != nil {
		m.models = msg.info
	}
	switch msg.action {
	case actionUse:
		if msg.err != nil {
			m.notice = "couldn't switch to " + msg.name + ": " + msg.err.Error()
			return
		}
		m.notice = joinNotice(msg.name+" answers now with "+msg.info.Main, msg.info.Warning)
		return
	case actionSave:
		if msg.err != nil {
			m.notice = "couldn't save the models: " + msg.err.Error()
			return
		}
		saved := msg.info.Main
		if msg.info.Active != "" {
			saved = msg.info.Active + " (" + msg.info.Main + ")"
		}
		m.notice = joinNotice("saved: merud starts with "+saved, msg.info.Warning)
		return
	}
	if b := m.modelBox; b != nil && b.loading {
		b.loading = false
		b.info = msg.info
		if msg.err != nil {
			b.err = msg.err.Error()
		}
	}
}

// joinNotice puts merud's warning after the notice, when there is one.
func joinNotice(notice, warning string) string {
	if warning == "" {
		return notice
	}
	return notice + " · " + warning
}

// activeSet returns the name of the model set in use, from merud's last
// models reply; "" when there is none or no reply has come.
func (m Model) activeSet() string {
	if m.models == nil {
		return ""
	}
	return m.models.Active
}

// modelBoxView draws the /model box for a pane width columns wide and
// height rows tall (see boxPane): the ModelTable lines, cut with "…"
// where the pane is too narrow for them.
func (m *Model) modelBoxView(width, height int) string {
	b := m.modelBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = []string{"merud gave no models: " + b.err}
	default:
		body = ModelTable(*b.info)
	}
	return m.boxPane("Model sets", body, modelNote, width, height)
}
