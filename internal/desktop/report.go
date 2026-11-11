// This file holds the Bridge's side of the anonymous daily usage report:
// asking how it stands, telling merud the page showed or closed the
// banner, and turning the report on or off. merud builds, sends and logs
// the report and writes the choice to config.toml (cmd/merud/usagereport.go);
// the Bridge only asks. See ARCHITECTURE.md, "Anonymous usage report".

package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// UsageReportView is what the page draws the banner and the Usage
// section from. Available is false when merud is too old to know the
// op; the page then shows neither. The other fields copy
// rpc.UsageReportStatus.
type UsageReportView struct {
	Available bool                   `json:"available"`
	Choice    string                 `json:"choice"`
	Blocked   string                 `json:"blocked"`
	Banner    string                 `json:"banner"`
	Next      string                 `json:"next"`
	NextAt    string                 `json:"next_at"`
	Version   int                    `json:"version"`
	Sent      bool                   `json:"sent"`
	URL       string                 `json:"url"`
	Recent    []rpc.UsageReportEntry `json:"recent"`
}

// UsageReport asks merud how the usage report stands: the choice, the
// banner to show, the next report and the recent lines of its log. A
// merud older than the op gives a view with Available false and no
// error. It fails when merud can't be reached or answers with another
// error.
func (b *Bridge) UsageReport(ctx context.Context) (UsageReportView, error) {
	return b.usageReport(ctx, rpc.UsageReportStatusKind)
}

// UsageReportMark tells merud the page showed the banner, with kind
// "seen", or that the user closed it, with "dismiss", and returns the
// view after. merud writes one "seen" line however often the page
// sends it. It fails for any other kind.
func (b *Bridge) UsageReportMark(ctx context.Context, kind string) (UsageReportView, error) {
	if kind != rpc.UsageReportSeen && kind != rpc.UsageReportDismiss {
		return UsageReportView{}, errors.New(`mark the banner "seen" or "dismiss"`)
	}
	return b.usageReport(ctx, kind)
}

// UsageReportSet turns the report on or off, with choice "on" or "off".
// merud writes [usage_report] send in config.toml and closes the banner,
// and the reply is the view after. It fails for any other choice.
func (b *Bridge) UsageReportSet(ctx context.Context, choice string) (UsageReportView, error) {
	if choice != rpc.UsageReportOn && choice != rpc.UsageReportOff {
		return UsageReportView{}, errors.New(`turn the report "on" or "off"`)
	}
	return b.usageReport(ctx, choice)
}

// usageReport sends the usage_report op with kind and turns the reply
// into a view.
func (b *Bridge) usageReport(ctx context.Context, kind string) (UsageReportView, error) {
	req := rpc.Request{Op: rpc.OpUsageReport, Kind: kind, Source: rpc.SourceDesktop}
	ev, err := b.one(ctx, req, rpc.EventUsageReport)
	if oldMerud(err) {
		return UsageReportView{Recent: []rpc.UsageReportEntry{}}, nil
	}
	if err != nil {
		return UsageReportView{}, err
	}
	if ev.UsageReport == nil {
		return UsageReportView{Recent: []rpc.UsageReportEntry{}}, nil
	}
	s := ev.UsageReport
	v := UsageReportView{
		Available: true,
		Choice:    s.Choice,
		Blocked:   s.Blocked,
		Banner:    s.Banner,
		Next:      s.Next,
		NextAt:    s.NextAt,
		Version:   s.Version,
		Sent:      s.Sent,
		URL:       s.URL,
		Recent:    s.Recent,
	}
	// JSON writes a nil slice as null; the page wants an empty list.
	if v.Recent == nil {
		v.Recent = []rpc.UsageReportEntry{}
	}
	return v, nil
}

// oldMerud reports whether err means merud is older than the usage_report
// op. Such a merud answers with the error event `unknown op
// "usage_report"`, which reaches the Bridge only as text, so the check
// reads the text; a merud that answers with some other event gives
// errNoReply.
func oldMerud(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errNoReply) || strings.HasPrefix(err.Error(), "unknown op")
}
