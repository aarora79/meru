// This file holds Status, the answer to the usage_report op: everything
// meru chat, Meru.app and `meru report` need to draw the notice and the
// Usage section.

package usagereport

import (
	"context"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// Status says what the clients should show at the moment: the choice, why
// nothing will send if something holds it back, whether to show the notice
// and which one, the next report as JSON, the earliest time it can go, and
// the last lines of the file, newest first. It fails when the file can't be
// read or the store can't count yesterday's questions.
func (r *Reporter) Status(ctx context.Context, s Settings) (rpc.UsageReportStatus, error) {
	now := r.now()
	st, err := readState(r.path, now)
	if err != nil {
		return rpc.UsageReportStatus{}, err
	}
	why := r.blocked(s, st, now)
	out := rpc.UsageReportStatus{
		Choice:  s.Send,
		Blocked: why,
		Version: Version,
		Sent:    st.sentEver,
		URL:     s.URL,
		Banner:  banner(s, st, doNotTrack(r.env("DO_NOT_TRACK"))),
		NextAt:  r.nextAt(why, now),
		CanSend: r.blockedNow(s, st, now) == "",
	}
	body, err := r.build(ctx, s, st, now)
	if err != nil {
		return rpc.UsageReportStatus{}, err
	}
	out.Next = string(body)
	for _, e := range st.recent {
		out.Recent = append(out.Recent, rpc.UsageReportEntry{
			Type: e.Type, At: e.At.Format(time.RFC3339), Client: e.Client, Body: e.Body, Error: e.Error,
		})
	}
	return out, nil
}

// banner picks the notice the clients show: rpc.BannerAsk while the user
// hasn't chosen, as after an upgrade; rpc.BannerNew for an install whose
// setup turned the report on; and none once the user closed it for this
// Version, turned the report off, or set DO_NOT_TRACK.
func banner(s Settings, st state, dnt bool) string {
	if dnt || st.dismissed {
		return ""
	}
	switch s.Send {
	case "":
		return rpc.BannerAsk
	case "on":
		return rpc.BannerNew
	}
	return ""
}

// nextAt returns the earliest time the next report can go, in RFC 3339, or
// "" when nothing will send until the user or a client acts. A report held
// to the next day goes at midnight UTC plus a minute not picked yet, so the
// time given is that midnight.
func (r *Reporter) nextAt(why string, now time.Time) string {
	today := startOfDay(now)
	switch why {
	case "":
		return now.UTC().Format(time.RFC3339)
	case reasonNotYet:
		minute := time.Duration(r.sendMinute(today)) * time.Minute
		return today.Add(minute).Format(time.RFC3339)
	case reasonFirstDay, reasonSentToday:
		return today.Add(24 * time.Hour).Format(time.RFC3339)
	}
	return ""
}
