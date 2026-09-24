// This file holds merud's skill service: it installs the built-in skills,
// keeps the skill registry current as you edit ~/.meru/skills, hands it to
// the agent each turn, and answers the skill ops behind `meru skills`. It
// also expands the "~" in [skills] output_dir for the write_file tool.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/skills"
)

// skillService owns the skills directory while merud runs.
//
// It picks up hand edits by comparing skills.Stamp with the stamp of its
// last load, on every turn and every skill op, and loads again when they
// differ. That costs a directory read and a stat per skill, microseconds
// for a handful of skills. A file watcher (fsnotify) would need a watch on
// the folder and on every skill folder in it, kept in step as folders come
// and go, plus a goroutine to own it; the stamp needs none of that.
type skillService struct {
	dir string // usually ~/.meru/skills
	log *slog.Logger

	mu    sync.Mutex       // guards reg and stamp; turns ask from many goroutines
	reg   *skills.Registry // the last load
	stamp string           // skills.Stamp(dir) just before that load
}

// newSkillService copies the built-in skills into dir where no folder of
// that name exists yet (your copy wins), loads the registry, and logs one
// warning per skill folder it skipped. It fails when dir can't be created
// or read, or a built-in can't be written; merud then refuses to start.
func newSkillService(dir string, log *slog.Logger) (*skillService, error) {
	installed, err := skills.InstallBuiltins(dir)
	if err != nil {
		return nil, err
	}
	if len(installed) > 0 {
		log.Info("built-in skills installed", "dir", dir, "skills", strings.Join(installed, ","))
	}
	s := &skillService{dir: dir, log: log}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Registry returns the current skill registry, loading it again first when
// the skills directory changed since the last load. It is the method the
// agent calls once per turn (agent.Skills). When the directory can't be
// read, it logs a warning and keeps the last registry, so a turn never
// fails over skills.
func (s *skillService) Registry(ctx context.Context) *skills.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamp, err := skills.Stamp(s.dir)
	if err != nil {
		s.log.WarnContext(ctx, "check skills directory; keeping the skills already loaded", "err", err)
		return s.reg
	}
	if stamp != s.stamp {
		if err := s.loadLocked(ctx); err != nil {
			s.log.WarnContext(ctx, "reload skills; keeping the skills already loaded", "err", err)
		}
	}
	return s.reg
}

// loadLocked loads the registry from s.dir and logs what it found. The
// caller must hold s.mu. It takes the stamp before loading: a file that
// changes during the load then changes the stamp again, and the next call
// loads once more instead of missing the edit.
func (s *skillService) loadLocked(ctx context.Context) error {
	stamp, err := skills.Stamp(s.dir)
	if err != nil {
		return err
	}
	reg, err := skills.Load(s.dir)
	if err != nil {
		return err
	}
	s.reg, s.stamp = reg, stamp
	for _, w := range reg.Warnings() {
		s.log.WarnContext(ctx, "skill skipped", "err", w)
	}
	names := make([]string, 0, len(reg.List()))
	for _, sum := range reg.List() {
		names = append(names, sum.Name)
	}
	s.log.InfoContext(ctx, "skills loaded", "dir", s.dir, "skills", strings.Join(names, ","),
		"skipped", len(reg.Warnings()))
	return nil
}

// handleList answers OpSkills with one "skills" event: every loaded skill
// with its description and its built-in and edited marks, and in Text the
// reasons merud skipped any folders, one per line. A built-in whose file
// it can't compare gets a warning in the log and no [edited] mark.
func (s *skillService) handleList(ctx context.Context, emit func(rpc.Event) error) error {
	reg := s.Registry(ctx)
	list := reg.List()
	infos := make([]rpc.SkillInfo, len(list))
	for i, sum := range list {
		edited, err := skills.Edited(s.dir, sum.Name)
		if err != nil {
			s.log.WarnContext(ctx, "compare skill with the shipped copy", "skill", sum.Name, "err", err)
		}
		infos[i] = rpc.SkillInfo{
			Name: sum.Name, Description: sum.Description,
			Builtin: skills.IsBuiltin(sum.Name), Edited: edited,
		}
	}
	var warnings []string
	for _, w := range reg.Warnings() {
		warnings = append(warnings, w.Error())
	}
	return emit(rpc.Event{Type: rpc.EventSkills, Skills: infos, Text: strings.Join(warnings, "\n")})
}

// handleShow answers OpSkillShow with one "skills" event holding the skill
// named req.ID and its whole SKILL.md in Body. It fails when no loaded
// skill has that name.
func (s *skillService) handleShow(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	reg := s.Registry(ctx)
	text, err := reg.File(req.ID)
	if errors.Is(err, skills.ErrNotFound) {
		return fmt.Errorf("no skill %q; meru skills list shows them", req.ID)
	}
	if err != nil {
		return err
	}
	sum, _ := reg.Get(req.ID)
	edited, err := skills.Edited(s.dir, req.ID)
	if err != nil {
		s.log.WarnContext(ctx, "compare skill with the shipped copy", "skill", req.ID, "err", err)
	}
	info := rpc.SkillInfo{
		Name: sum.Name, Description: sum.Description,
		Builtin: skills.IsBuiltin(sum.Name), Edited: edited, Body: text,
	}
	return emit(rpc.Event{Type: rpc.EventSkills, Skills: []rpc.SkillInfo{info}})
}

// joinAnd joins names as English does: "a", "a and b", "a, b and c".
func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// handleReset answers OpSkillReset: it puts the shipped copy of the
// built-in skill named req.ID back in the skills directory and loads the
// registry again. The reply is "done" alone. It fails for a skill Meru
// doesn't ship, and when the files can't be written.
func (s *skillService) handleReset(ctx context.Context, req rpc.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := skills.Reset(s.dir, req.ID)
	if errors.Is(err, skills.ErrNotBuiltin) {
		return fmt.Errorf("%q isn't a built-in skill; reset restores only %s", req.ID, joinAnd(skills.Builtins()))
	}
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "skill reset to the shipped copy", "skill", req.ID)
	return s.loadLocked(ctx)
}

// expandHome turns "~" or "~/x" into a path under the home directory and
// cleans any other path. Config has already checked that the path is
// absolute or starts with "~". It fails when the OS can't say where home
// is.
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return filepath.Join(home, p[1:]), nil
	}
	return filepath.Clean(p), nil
}
