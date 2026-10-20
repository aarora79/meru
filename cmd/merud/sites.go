// This file holds the websites web_fetch reads without asking in every
// session, [web] trusted_sites: saving one when the user picks "Always
// this website" at a prompt, and the trusted_sites and trusted_site_remove
// ops, which list them for Settings and `/sites` and take one out. See
// ARCHITECTURE.md, "Web search".

package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// saveTrustedSite adds host to [web] trusted_sites in config.toml, unless
// it is there already, and hands the new list to the built-ins. It holds
// the lock every config.toml write takes. It fails for a host that isn't
// a bare host name, or a config that doesn't load or write.
func (s *toolService) saveTrustedSite(ctx context.Context, host string) error {
	if err := config.CheckTrustedSite(host); err != nil {
		return err
	}
	err := s.bt.EditConfig(func() error {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		if slices.Contains(cfg.Web.TrustedSites, host) {
			s.bt.UseTrustedSites(cfg.Web.TrustedSites)
			return nil
		}
		sites := append(slices.Clone(cfg.Web.TrustedSites), host)
		return s.writeTrustedSites(sites)
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "trusted site added", "site", host)
	return nil
}

// writeTrustedSites writes sites as [web] trusted_sites through the
// catalog's checked edit, which keeps every comment and replaces the file
// only once the result loads with that list, then hands the list to the
// built-ins. The caller holds the config lock.
func (s *toolService) writeTrustedSites(sites []string) error {
	err := catalog.SetTableLists(s.configPath, "web", map[string][]string{"trusted_sites": sites}, func(c config.Config) error {
		if !slices.Equal(c.Web.TrustedSites, sites) {
			return fmt.Errorf("[web] trusted_sites came out as %v", c.Web.TrustedSites)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.bt.UseTrustedSites(sites)
	return nil
}

// handleTrustedSites answers OpTrustedSites with one "trusted_sites" event
// listing [web] trusted_sites, as config.toml holds it now.
func (s *toolService) handleTrustedSites(emit func(rpc.Event) error) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventTrustedSites, Sites: cfg.Web.TrustedSites})
}

// handleTrustedSiteRemove answers OpTrustedSiteRemove: it takes the host
// in req.ID out of [web] trusted_sites, so web_fetch asks again before it
// reads that site in a new session, and replies with the list left. A
// session that trusted the site for itself keeps that trust. It fails for
// a host the list doesn't hold.
func (s *toolService) handleTrustedSiteRemove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	var left []string
	err := s.bt.EditConfig(func() error {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		if !slices.Contains(cfg.Web.TrustedSites, req.ID) {
			return fmt.Errorf("[web] trusted_sites doesn't list %q", req.ID)
		}
		left = slices.DeleteFunc(slices.Clone(cfg.Web.TrustedSites), func(h string) bool { return h == req.ID })
		return s.writeTrustedSites(left)
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "trusted site removed", "site", req.ID)
	return emit(rpc.Event{Type: rpc.EventTrustedSites, Sites: left})
}
