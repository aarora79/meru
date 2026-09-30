// This file holds the "Obsidian notes" step. It asks for the vault folder
// and hands it to the Start Meru step, which asks merud to turn the
// Obsidian connector on with it; merud installs the pinned obsidian-mcp
// with its own Node. An obsidian entry the user set up by hand in
// [[mcp.servers]] stays as it is, unless the user picks Adopt, which moves
// it over to the connector. See ARCHITECTURE.md, "Installer".

package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// ObsidianInput is what the user chose on the Obsidian screen: the vault
// folder, or, for an entry set up by hand, whether to adopt it.
type ObsidianInput struct {
	Vault string `json:"vault"`
	Adopt bool   `json:"adopt"`
}

// ByHand describes a connector's state in config.toml for a screen:
// whether an [[mcp.servers]] entry of its name runs it by hand, and
// whether its [connectors.<id>] table turns it on.
type ByHand struct {
	Entry bool
	On    bool
}

// connectorState reads connector id's state from cfg.
func connectorState(cfg config.Config, id string) ByHand {
	on, _ := cfg.Connectors[id].Enabled()
	return ByHand{Entry: hasServer(cfg, id), On: on}
}

// ObsidianFound says what of the Obsidian step is in place, for a second
// run, or "" when nothing is.
func ObsidianFound(cfg config.Config) string {
	st := connectorState(cfg, "obsidian")
	switch {
	case st.Entry:
		return "config.toml has an obsidian entry you set up by hand. It keeps working; Adopt lets Meru run it instead."
	case st.On:
		vault, _ := cfg.Connectors["obsidian"].Value("vault_path")
		return "Meru runs Obsidian with the vault " + vault + "."
	}
	return ""
}

// SetUpObsidian runs the Obsidian step with in, given the connector's
// state in config.toml, and returns what it did, with the hand-off for the
// Start Meru step. An entry set up by hand is adopted when in says so and
// left alone otherwise. A vault must be a folder on this Mac; one written
// "~/…" counts from the home folder, and goes to merud as written.
func SetUpObsidian(p Paths, st ByHand, in ObsidianInput) (string, *HandOff, error) {
	if st.Entry {
		if !in.Adopt {
			return "Your own obsidian entry stays as it is. Meru.app's Settings, Connections, can move it over later.", nil, nil
		}
		return "When Meru starts, merud moves your obsidian entry over to its connector: it keeps the vault and the tools the entry allows, and comments the entry out, so meru mcp unadopt obsidian can put it back.",
			&HandOff{ID: "obsidian", Name: "Obsidian", Adopt: true}, nil
	}
	vault := strings.TrimSpace(in.Vault)
	if vault == "" {
		return "", nil, errors.New("pick your vault folder first, or skip Obsidian")
	}
	full := vault
	if rest, ok := strings.CutPrefix(vault, "~/"); ok {
		full = filepath.Join(p.Home, rest)
	}
	if info, err := os.Stat(full); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("%s isn't a folder on this Mac; pick the folder that holds your notes", vault)
	}
	on := true
	hand := &HandOff{ID: "obsidian", Name: "Obsidian", Change: &rpc.ConnectorChange{Enabled: &on, Values: map[string]string{"vault_path": vault}}}
	return "When Meru starts, merud installs obsidian-mcp at the version pinned in this release, with its own Node, and reads the vault " +
		p.Tilde(full) + ". Obsidian itself needn't run.", hand, nil
}
