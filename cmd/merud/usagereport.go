// This file answers the usage_report op and feeds the anonymous daily usage
// report its settings: whether [usage_report] send is on, and where it
// goes. The rule for when to send, and the record in usage-report.jsonl,
// live in internal/usagereport. See ARCHITECTURE.md, "Anonymous usage
// report".

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/usagereport"
)

// reportService holds what the usage_report op needs: the reporter, the
// config file and the lock every config.toml write takes.
type reportService struct {
	reporter   *usagereport.Reporter
	configPath string
	editConfig func(func() error) error
	log        *slog.Logger
}

// settings reads [usage_report] and the profile from config.toml as it
// stands, so a change from `meru report off`, the Settings switch or a
// hand edit takes effect without a restart. It fails when the file
// doesn't load.
func (s *reportService) settings() (usagereport.Settings, error) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return usagereport.Settings{}, err
	}
	return usagereport.Settings{Send: cfg.UsageReport.Send, URL: cfg.UsageReport.URL, Profile: cfg.Profile}, nil
}

// handle answers OpUsageReport. Every action replies with the status after
// it, so a client redraws from one answer. "send" sends today's report now,
// if the rules allow it; otherwise the client gets the reason as an error.
// "on" and "off" write the choice to config.toml first and add the
// "dismissed" line second: if merud stops between the two, the notice shows
// again, which is the safe way to fail. "on" also marks the notice seen,
// since the user who turns the report on knows of it; otherwise "on" before
// any client showed the banner would hide the banner for good, and merud,
// which waits for "seen", would never send.
func (s *reportService) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	switch req.Kind {
	case "", rpc.UsageReportStatusKind:
		// A status check changes nothing; the reply below is the status.
	case rpc.UsageReportSeen:
		if err := s.reporter.Mark(usagereport.EntrySeen, string(req.Source)); err != nil {
			return err
		}
	case rpc.UsageReportDismiss:
		if err := s.reporter.Mark(usagereport.EntryDismissed, string(req.Source)); err != nil {
			return err
		}
	case rpc.UsageReportSend:
		set, err := s.settings()
		if err != nil {
			return err
		}
		if err := s.reporter.SendNow(ctx, set); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "usage report sent by hand")
	case rpc.UsageReportOn, rpc.UsageReportOff:
		if err := s.setSend(req.Kind); err != nil {
			return err
		}
		if req.Kind == rpc.UsageReportOn {
			if err := s.reporter.Mark(usagereport.EntrySeen, string(req.Source)); err != nil {
				return err
			}
		}
		if err := s.reporter.Mark(usagereport.EntryDismissed, string(req.Source)); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "usage report choice", "send", req.Kind)
	default:
		return fmt.Errorf("usage_report: unknown kind %q", req.Kind)
	}
	set, err := s.settings()
	if err != nil {
		return err
	}
	status, err := s.reporter.Status(ctx, set)
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventUsageReport, UsageReport: &status})
}

// setSend writes [usage_report] send = value through the catalog's checked
// edit, which keeps every comment and replaces the file only once the
// result loads with that value. It holds the config lock.
func (s *reportService) setSend(value string) error {
	return s.editConfig(func() error {
		return catalog.SetTableString(s.configPath, "usage_report", "send", value, func(c config.Config) error {
			if c.UsageReport.Send != value {
				return fmt.Errorf("[usage_report] send came out as %q", c.UsageReport.Send)
			}
			return nil
		})
	})
}
