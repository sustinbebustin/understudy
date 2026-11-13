package cloudwatch

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
	"github.com/sustinbebustin/understudy/internal/store"
)

// historyRetention is how long alarm history is kept on the emulator
// clock: "CloudWatch preserves alarm history for 30 days"
// (cloudwatch-alarms.md 8.1).
const historyRetention = 30 * 24 * time.Hour

// historyPrefix starts the section path of every history cell, beside the
// state sections "<account>/<region>.json".
const historyPrefix = "history/"

func newHistory(aws.Scope) History { return History{Items: map[string][]HistoryItem{}} }

// IsZero reports a scope without history, which snapshots skip.
func (h *History) IsZero() bool { return len(h.Items) == 0 }

var _ store.Zeroer = (*History)(nil)

// historyKeys is the history partition's section codec:
// "history/<account>/<region>".
func historyKeys() store.KeyCodec[aws.Scope] {
	scopes := store.ScopeKeys()
	return store.KeyCodec[aws.Scope]{
		Encode: func(s aws.Scope) string {
			enc := scopes.Encode(s)
			if enc == "" {
				return ""
			}
			return historyPrefix + enc
		},
		Decode: func(enc string) (aws.Scope, error) {
			rest, ok := strings.CutPrefix(enc, historyPrefix)
			if !ok {
				return aws.Scope{}, &store.KeyLayoutError{Encoded: enc, Layout: historyPrefix + "<account>/<region>"}
			}
			return scopes.Decode(rest)
		},
	}
}

// splitHistory separates the history sections from the state sections.
func splitHistory(ss []store.Section) (state, history []store.Section) {
	for _, sec := range ss {
		if strings.HasPrefix(sec.Path, historyPrefix) {
			history = append(history, sec)
		} else {
			state = append(state, sec)
		}
	}
	return state, history
}

// historyEntry is one item to append to the history of the alarm name.
type historyEntry struct {
	name string
	item HistoryItem
}

// appendHistory writes entries, in order, to their alarms' history in
// scope, drops the scope's items older than historyRetention at the
// write's instant, and applies effects, a transition's actions, in the
// same write, so they follow the items (cloudwatch-alarms.md 12.4 item 5).
//
// It is a second write after the state write that made the change: a
// snapshot cut between the two saves the new state without its history
// items or actions, and they are lost. Merging the two stores would make
// history part of the alarm's lifetime, which AWS's history outlives
// (cloudwatch-alarms.md 8.1).
func (s *Service) appendHistory(c *kernel.Call, scope aws.Scope, entries []historyEntry, effects []func(tx *kernel.Tx)) *awserr.Error {
	_, err := kernel.Write(c, s.history, scope, func(h *History, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		for _, apply := range effects {
			apply(tx)
		}
		cutoff := now.Add(-historyRetention)
		for n, items := range h.Items {
			kept := slices.DeleteFunc(slices.Clone(items), func(it HistoryItem) bool { return it.At.Before(cutoff) })
			if len(kept) == 0 {
				delete(h.Items, n)
			} else {
				h.Items[n] = kept
			}
		}
		for _, e := range entries {
			h.Items[e.name] = append(slices.Clone(h.Items[e.name]), e.item)
		}
		return struct{}{}, nil
	})
	return err
}

// configUpdate is the ConfigurationUpdate item of a create, update or
// delete of the alarm ref, written at now: a is the alarm after the
// change, or as it was when deleted, and previous the replaced alarm on an
// update, nil otherwise (cloudwatch-alarms.md 8.3).
func configUpdate(op alarm.ConfigOp, ref alarmRef, a, previous *Alarm, now clock.Instant) historyEntry {
	var prev *alarm.Subject
	if previous != nil {
		prev = new(subjectOf(ref, previous))
	}
	summary, data := alarm.ConfigurationUpdate(op, subjectOf(ref, a), prev, a.State.record())
	return historyEntry{name: ref.name, item: HistoryItem{At: now, Type: cloudwatchapi.HistoryItemTypeConfigurationUpdate, Summary: summary, Data: data}}
}

// appendConfigUpdates appends the ConfigurationUpdate items of committed
// configuration changes in the caller's scope. A failed write is logged
// rather than returned: the changes have committed.
func (s *Service) appendConfigUpdates(ctx context.Context, c *kernel.Call, entries []historyEntry) {
	if len(entries) == 0 {
		return
	}
	if err := s.appendHistory(c, c.Scope(), entries, nil); err != nil {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.name
		}
		s.k.Log().LogAttrs(ctx, slog.LevelError, "the history write of an alarm configuration change failed; the change stands, and its ConfigurationUpdate history item is lost",
			slog.String("alarms", strings.Join(names, ",")), slog.String("error", err.Message))
	}
}

// describeAlarmHistory lists the caller's StateUpdate and
// ConfigurationUpdate items of metric alarms, newest first unless ScanBy
// says otherwise (cloudwatch-alarms.md 8.1). understudy records no Action
// items and no contributors (cloudwatch-alarms.md 8.3), so asking for them
// selects nothing. Items older than historyRetention are never returned.
func (s *Service) describeAlarmHistory(_ context.Context, c *kernel.Call, in *cloudwatchapi.DescribeAlarmHistoryInput) (*cloudwatchapi.DescribeAlarmHistoryOutput, *awserr.Error) {
	out := &cloudwatchapi.DescribeAlarmHistoryOutput{AlarmHistoryItems: []cloudwatchapi.AlarmHistoryItem{}}
	metric := len(in.AlarmTypes) == 0 || slices.Contains(in.AlarmTypes, cloudwatchapi.AlarmTypeMetricAlarm)
	var rows []historyRow
	if cell, ok := s.history.Lookup(c.Scope()); ok && metric && in.AlarmContributorId == nil {
		cutoff := c.Admitted().Add(-historyRetention)
		rows = store.Read(cell, func(h *History) []historyRow { return h.selected(in, cutoff) })
	}
	slices.SortFunc(rows, func(a, b historyRow) int { return cmp.Compare(a.key, b.key) })
	limit := defaultMaxRecords // cloudwatch.alarm.history.page-size
	if in.MaxRecords != nil {
		limit = int(*in.MaxRecords)
	}
	page, next, ok := pageOf(rows, func(r historyRow) string { return r.key }, in.NextToken, limit)
	if !ok {
		return nil, errInvalidNextToken()
	}
	for _, r := range page {
		out.AlarmHistoryItems = append(out.AlarmHistoryItems, cloudwatchapi.AlarmHistoryItem{
			AlarmName:       new(r.name),
			AlarmType:       new(cloudwatchapi.AlarmTypeMetricAlarm),
			Timestamp:       new(r.item.At.Wire()),
			HistoryItemType: new(r.item.Type),
			HistorySummary:  new(r.item.Summary),
			HistoryData:     new(r.item.Data),
		})
	}
	out.NextToken = next
	return out, nil
}

// selected are the items DescribeAlarmHistory's name, item type and date
// filters select, none written before cutoff, each with its page key.
func (h *History) selected(in *cloudwatchapi.DescribeAlarmHistoryInput, cutoff clock.Instant) []historyRow {
	ascending := in.ScanBy != nil && *in.ScanBy == cloudwatchapi.ScanByTimestampAscending
	var sel []historyRow
	for name, items := range h.Items {
		if in.AlarmName != nil && *in.AlarmName != name {
			continue
		}
		for i, it := range items {
			if in.HistoryItemType != nil && *in.HistoryItemType != it.Type {
				continue
			}
			if at := it.At.Wire(); !it.At.Before(cutoff) && inDates(at, in.StartDate, in.EndDate) {
				sel = append(sel, historyRow{name: name, item: it, key: historyKey(at, name, i, ascending)})
			}
		}
	}
	return sel
}

// inDates reports whether at is within StartDate and EndDate, both
// inclusive (inferred: the API reference does not say).
func inDates(at time.Time, start, end *time.Time) bool {
	return (start == nil || !at.Before(*start)) && (end == nil || !at.After(*end))
}

// historyRow is one item selected for a listing, with its page key.
type historyRow struct {
	name string
	item HistoryItem
	key  string
}

// historyKey orders a listing and keys its pages: by timestamp, then by
// alarm name, then by write order within the alarm, with the timestamp and
// the write order inverted for a descending scan, so equal timestamps of
// one alarm list the newest written first. The order of
// equal timestamps across alarms is undocumented; by name is understudy's.
func historyKey(at time.Time, name string, index int, ascending bool) string {
	t, i := at.UnixNano(), int64(index)
	if !ascending {
		t, i = math.MaxInt64-t, math.MaxInt64-i
	}
	return fmt.Sprintf("%020d/%s/%020d", t, name, i)
}
