// This file holds the Bridge methods behind the Library and Setup screens:
// connections and tool policies, folders, what Meru knows about you,
// skills, models and the answer model, activity and usage. Each one sends
// one request to merud and hands back its reply. merud makes every change;
// the app writes no file of Meru's (ARCHITECTURE.md, "Desktop app").

package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// defaultActivity is how many tool calls the Activity section shows.
const defaultActivity = 100

// ConnectionsView is the Library's Connections section: every tool source
// with each tool's policy, and the catalog servers the user can add.
type ConnectionsView struct {
	Connections []rpc.Connection   `json:"connections"`
	Catalog     []rpc.CatalogEntry `json:"catalog"`
}

// FoldersView is the Library's Folders section and the Setup screen's
// folder step: the [index] folders, and the usual ones not indexed yet.
type FoldersView struct {
	Folders   []rpc.FolderInfo `json:"folders"`
	Suggested []rpc.FolderInfo `json:"suggested"`
}

// SkillsView is the Library's Skills section. Warnings lists the skill
// folders merud skipped, and why.
type SkillsView struct {
	Skills   []rpc.SkillInfo `json:"skills"`
	Warnings []string        `json:"warnings"`
}

// Connections asks merud for every tool source and the catalog. It fails
// when merud can't be reached or answers with an error.
func (b *Bridge) Connections(ctx context.Context) (ConnectionsView, error) {
	return b.connections(ctx, rpc.Request{Op: rpc.OpConnections})
}

// SetPolicy sets one tool's policy: kind is "mcp", "a2a" or "builtin",
// server the source's name ("meru" for a built-in), and policy "off",
// "ask" or "allow". merud checks the change, writes config.toml and
// reloads the tools; the reply is the Connections view after the change.
func (b *Bridge) SetPolicy(ctx context.Context, kind, server, tool, policy string) (ConnectionsView, error) {
	change := rpc.PolicyChange{Kind: kind, Server: server, Tool: tool, Policy: policy}
	return b.connections(ctx, rpc.Request{Op: rpc.OpToolPolicy, Policy: &change})
}

// AddConnection adds the catalog server name, as `meru mcp add` would.
// When the server needs an API key, secret names it and key holds it:
// merud saves the key to secrets.toml first, then adds the server. An
// empty key keeps the one secrets.toml holds already. The key goes to
// merud over the socket, which only this user can open, and never comes
// back.
func (b *Bridge) AddConnection(ctx context.Context, name, secret, key string) (ConnectionsView, error) {
	if strings.TrimSpace(key) != "" {
		if err := b.SetSecret(ctx, secret, key); err != nil {
			return ConnectionsView{}, err
		}
	}
	return b.connections(ctx, rpc.Request{Op: rpc.OpMCPAdd, ID: name})
}

// AddCustomServer adds an MCP server of the user's own, from the Library's
// "Add your own MCP server" form, as `meru mcp add stdio` or `meru mcp add
// http` would. merud checks everything, saves each secret variable in
// secrets.toml, writes the entry with no tools allowed and reloads, so the
// reply lists the new server with every tool off. A secret's value goes
// to merud over the socket and never comes back. It fails when the name
// is blank, or with merud's reason when merud refuses.
func (b *Bridge) AddCustomServer(ctx context.Context, s rpc.CustomServer) (ConnectionsView, error) {
	if strings.TrimSpace(s.Name) == "" {
		return ConnectionsView{}, errors.New("give the server a name first")
	}
	return b.connections(ctx, rpc.Request{Op: rpc.OpMCPAdd, Custom: &s})
}

// RemoveConnection takes the MCP server name out of config.toml. Its key
// stays in secrets.toml, as after `meru mcp remove`.
func (b *Bridge) RemoveConnection(ctx context.Context, name string) (ConnectionsView, error) {
	return b.connections(ctx, rpc.Request{Op: rpc.OpMCPRemove, ID: name})
}

// SetSecret saves value as the secret name in secrets.toml, through merud,
// which accepts only a name a server uses. It fails on an empty value.
func (b *Bridge) SetSecret(ctx context.Context, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("paste the key first")
	}
	_, err := b.done(ctx, rpc.Request{Op: rpc.OpSecretSet, ID: name, Text: value})
	return err
}

// connections sends req and returns the "connections" reply as a view.
func (b *Bridge) connections(ctx context.Context, req rpc.Request) (ConnectionsView, error) {
	ev, err := b.one(ctx, req, rpc.EventConnections)
	if err != nil {
		return ConnectionsView{}, err
	}
	v := ConnectionsView{Connections: ev.Connections, Catalog: ev.Catalog}
	if v.Connections == nil {
		v.Connections = []rpc.Connection{}
	}
	// A server that isn't connected and has no tool allowed, such as one
	// the user just added by hand, lists no tools; the page wants [].
	for i := range v.Connections {
		if v.Connections[i].Tools == nil {
			v.Connections[i].Tools = []rpc.ToolPolicy{}
		}
	}
	if v.Catalog == nil {
		v.Catalog = []rpc.CatalogEntry{}
	}
	return v, nil
}

// Folders asks merud for the [index] folders and the suggested ones.
func (b *Bridge) Folders(ctx context.Context) (FoldersView, error) {
	return b.folders(ctx, rpc.Request{Op: rpc.OpFolders})
}

// AddFolder adds path to [index] folders; merud starts indexing it at
// once. path is a full path, as the folder dialog gives, or "~/..." as a
// suggestion names it.
func (b *Bridge) AddFolder(ctx context.Context, path string) (FoldersView, error) {
	return b.folders(ctx, rpc.Request{Op: rpc.OpFolderAdd, Path: path})
}

// RemoveFolder takes path, as the Folders view names it, out of [index]
// folders; merud drops its files from the index.
func (b *Bridge) RemoveFolder(ctx context.Context, path string) (FoldersView, error) {
	return b.folders(ctx, rpc.Request{Op: rpc.OpFolderRemove, Path: path})
}

// folders sends req and returns the "folders" reply as a view. Adding a
// folder counts files, so it may take a few seconds; it gets the longer
// timeout.
func (b *Bridge) folders(ctx context.Context, req rpc.Request) (FoldersView, error) {
	ev, err := b.one(ctx, req, rpc.EventFolders)
	if err != nil {
		return FoldersView{}, err
	}
	v := FoldersView{Folders: ev.Folders, Suggested: ev.Suggested}
	if v.Folders == nil {
		v.Folders = []rpc.FolderInfo{}
	}
	if v.Suggested == nil {
		v.Suggested = []rpc.FolderInfo{}
	}
	return v, nil
}

// Memories returns every memory file, for "About you". The page shows the
// profile kinds, "me" and "preferences", which go into every prompt.
func (b *Bridge) Memories(ctx context.Context) ([]rpc.MemoryInfo, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpMemoryList}, rpc.EventMemories)
	if err != nil {
		return nil, err
	}
	if ev.Memories == nil {
		return []rpc.MemoryInfo{}, nil
	}
	return ev.Memories, nil
}

// AddMemory saves text as a new memory of kind, such as "me" or
// "preferences", as `meru setup user` does, and returns it.
func (b *Bridge) AddMemory(ctx context.Context, kind, text string) (rpc.MemoryInfo, error) {
	if strings.TrimSpace(text) == "" {
		return rpc.MemoryInfo{}, errors.New("type something to remember first")
	}
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpMemoryAdd, Kind: kind, Text: text}, rpc.EventMemories)
	if err != nil {
		return rpc.MemoryInfo{}, err
	}
	if len(ev.Memories) == 0 {
		return rpc.MemoryInfo{}, errors.New("merud saved nothing")
	}
	return ev.Memories[0], nil
}

// ForgetMemory deletes the memory id, such as "people/sam-is-my-manager.md".
// An edit in "About you" is a forget and an add.
func (b *Bridge) ForgetMemory(ctx context.Context, id string) error {
	_, err := b.done(ctx, rpc.Request{Op: rpc.OpMemoryForget, ID: id})
	return err
}

// Skills lists the skills, the disabled ones last.
func (b *Bridge) Skills(ctx context.Context) (SkillsView, error) {
	return b.skills(ctx, rpc.Request{Op: rpc.OpSkills})
}

// SetSkill turns the skill name on or off, in [skills] disabled.
func (b *Bridge) SetSkill(ctx context.Context, name string, on bool) (SkillsView, error) {
	op := rpc.OpSkillDisable
	if on {
		op = rpc.OpSkillEnable
	}
	return b.skills(ctx, rpc.Request{Op: op, ID: name})
}

// skills sends req and returns the "skills" reply as a view.
func (b *Bridge) skills(ctx context.Context, req rpc.Request) (SkillsView, error) {
	ev, err := b.one(ctx, req, rpc.EventSkills)
	if err != nil {
		return SkillsView{}, err
	}
	v := SkillsView{Skills: ev.Skills, Warnings: []string{}}
	if v.Skills == nil {
		v.Skills = []rpc.SkillInfo{}
	}
	if ev.Text != "" {
		v.Warnings = strings.Split(ev.Text, "\n")
	}
	return v, nil
}

// Models asks merud which models config names, which Ollama holds in
// memory, and which of the models we tried Ollama has.
func (b *Bridge) Models(ctx context.Context) (rpc.ModelsInfo, error) {
	return b.models(ctx, rpc.Request{Op: rpc.OpModels})
}

// UseModel asks merud to make name the answer model, the Library's "Use
// for answers" button. merud writes [models] main and answers the next
// question with it, with no restart. The reply is the Models view after
// the change, with a Warning when the model can't call tools. It fails
// with merud's reason when merud refuses, as it does for a model Ollama
// doesn't have.
//
// The status block names the answer model, so the Bridge keeps the new
// name for it.
func (b *Bridge) UseModel(ctx context.Context, name string) (rpc.ModelsInfo, error) {
	if strings.TrimSpace(name) == "" {
		return rpc.ModelsInfo{}, errors.New("pick a model first")
	}
	info, err := b.models(ctx, rpc.Request{Op: rpc.OpModelSet, ID: name})
	if err != nil {
		return rpc.ModelsInfo{}, err
	}
	b.mu.Lock()
	b.model = info.Main
	b.mu.Unlock()
	return info, nil
}

// models sends req and returns the "models" reply. Choices comes back as
// [] rather than null, which the page's code can't loop over, and so does
// each choice's Capabilities and Tiers.
func (b *Bridge) models(ctx context.Context, req rpc.Request) (rpc.ModelsInfo, error) {
	ev, err := b.one(ctx, req, rpc.EventModels)
	if err != nil {
		return rpc.ModelsInfo{}, err
	}
	if ev.Models == nil {
		return rpc.ModelsInfo{}, errors.New("merud sent no models")
	}
	info := *ev.Models
	if info.Choices == nil {
		info.Choices = []rpc.ModelChoice{}
	}
	for i := range info.Choices {
		c := &info.Choices[i]
		if c.Capabilities == nil {
			c.Capabilities = []string{}
		}
		if c.Tiers == nil {
			c.Tiers = []string{}
		}
	}
	return info, nil
}

// Activity returns the latest tool calls from the audit log, newest first:
// the data `meru log` prints.
func (b *Bridge) Activity(ctx context.Context) ([]rpc.LogEntry, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpLog, Limit: defaultActivity}, rpc.EventLog)
	if err != nil {
		return nil, err
	}
	if ev.Log == nil {
		return []rpc.LogEntry{}, nil
	}
	return ev.Log, nil
}

// Usage returns how much Meru has been used, window by window, as the
// chat's /usage box shows it.
func (b *Bridge) Usage(ctx context.Context) ([]rpc.UsageWindow, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpUsage}, rpc.EventUsage)
	if err != nil {
		return nil, err
	}
	return ev.Usage, nil
}
