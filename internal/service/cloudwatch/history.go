package cloudwatch

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
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

// appendHistory writes item to the alarm's history in scope, drops the
// scope's items older than historyRetention at the write's instant, and
// applies effects, the transition's actions, in the same write, so they
// follow the item (cloudwatch-alarms.md 12.4 item 5).
//
// It is a second write after the state write that made the transition: a
// snapshot cut between the two saves the new state without its history
// item or actions, and they are lost. Merging the two stores would make
// history part of the alarm's lifetime, which AWS's history outlives
// (cloudwatch-alarms.md 8.1).
func (s *Service) appendHistory(c *kernel.Call, scope aws.Scope, name string, item HistoryItem, effects []func(tx *kernel.Tx)) *awserr.Error {
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
		h.Items[name] = append(slices.Clone(h.Items[name]), item)
		return struct{}{}, nil
	})
	return err
}

// describeAlarmHistory lists the caller's StateUpdate items of metric
// alarms, newest first unless ScanBy says otherwise (cloudwatch-alarms.md
// 8.1). understudy records no ConfigurationUpdate or Action items and no
// contributors (cloudwatch-alarms.md 8.3), so asking for them selects
// nothing. Items older than historyRetention are never returned.
func (s *Service) describeAlarmHistory(_ context.Context, c *kernel.Call, in *cloudwatchapi.DescribeAlarmHistoryInput) (*cloudwatchapi.DescribeAlarmHistoryOutput, *awserr.Error) {
	out := &cloudwatchapi.DescribeAlarmHistoryOutput{AlarmHistoryItems: []cloudwatchapi.AlarmHistoryItem{}}
	metric := len(in.AlarmTypes) == 0 || slices.Contains(in.AlarmTypes, cloudwatchapi.AlarmTypeMetricAlarm)
	stateUpdates := in.HistoryItemType == nil || *in.HistoryItemType == cloudwatchapi.HistoryItemTypeStateUpdate
	var rows []historyRow
	if cell, ok := s.history.Lookup(c.Scope()); ok && metric && stateUpdates && in.AlarmContributorId == nil {
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
			HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate),
			HistorySummary:  new(r.item.Summary),
			HistoryData:     new(r.item.Data),
		})
	}
	out.NextToken = next
	return out, nil
}

// selected are the items DescribeAlarmHistory's name and date filters
// select, none written before cutoff, each with its page key.
func (h *History) selected(in *cloudwatchapi.DescribeAlarmHistoryInput, cutoff clock.Instant) []historyRow {
	ascending := in.ScanBy != nil && *in.ScanBy == cloudwatchapi.ScanByTimestampAscending
	var sel []historyRow
	for name, items := range h.Items {
		if in.AlarmName != nil && *in.AlarmName != name {
			continue
		}
		for i, it := range items {
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
