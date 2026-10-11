package logs

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/logsfilter"
)

// filtered is one matching event of a FilterLogEvents listing.
type filtered struct {
	stream string
	event  Event
}

// filterLogEvents lists the matching events of a group's selected streams,
// interleaved by position, one page at a time (secrets-logs-events-alarms.md
// 3.5, logs-filter-patterns.md 6). Both time bounds are inclusive, by the
// literal documentation. The token is a cursor over positions, so a page
// stays correct when events arrive or expire between calls; nextToken is
// absent once no matching event remains.
func (s *Service) filterLogEvents(_ context.Context, c *kernel.Call, in *logsapi.FilterLogEventsInput) (*logsapi.FilterLogEventsOutput, *awserr.Error) {
	ref, err := resolveGroup(c, in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	q, err := s.parseFilterQuery(in)
	if err != nil {
		return nil, err
	}
	matches, ok := group(s, ref, func(g *Group) []filtered {
		var out []filtered
		for _, name := range g.streamNames() {
			if !selects(in, name) {
				continue
			}
			for _, e := range g.Streams[name].Events {
				if q.matches(&e) {
					out = append(out, filtered{stream: name, event: e})
				}
			}
		}
		slices.SortFunc(out, func(a, b filtered) int { return a.event.pos().compare(b.event.pos()) })
		return out
	})
	if !ok {
		return nil, errGroupNotFound()
	}
	page, next := filterPage(matches, q.cur, q.limit)
	out := &logsapi.FilterLogEventsOutput{
		Events:             make([]logsapi.FilteredLogEvent, len(page)),
		SearchedLogStreams: []logsapi.SearchedLogStream{},
	}
	for i, m := range page {
		out.Events[i] = logsapi.FilteredLogEvent{
			LogStreamName: new(m.stream),
			Timestamp:     new(m.event.Timestamp),
			Message:       new(m.event.Message),
			IngestionTime: new(m.event.Ingestion),
			EventId:       new(m.event.pos().String()),
		}
	}
	if next != nil {
		out.NextToken = new(next.String())
	}
	return out, nil
}

// filterQuery is a parsed FilterLogEvents request: the compiled pattern,
// the inclusive time range, the cursor and the page limit.
type filterQuery struct {
	filter *logsfilter.Filter
	lo, hi int64
	cur    cursor
	limit  int
}

// parseFilterQuery compiles the pattern and reads the paging members; the
// first page of startFromHead=false starts at the newest event.
func (s *Service) parseFilterQuery(in *logsapi.FilterLogEventsInput) (filterQuery, *awserr.Error) {
	var pattern string
	if in.FilterPattern != nil {
		pattern = *in.FilterPattern
	}
	f, perr := s.filter.Compile(pattern)
	if perr != nil {
		return filterQuery{}, errFilterPattern(perr.Message())
	}
	q := filterQuery{filter: f, hi: math.MaxInt64, cur: cursor{dir: forward}, limit: maxPageEvents}
	if in.StartTime != nil {
		q.lo = *in.StartTime
	}
	if in.EndTime != nil {
		q.hi = *in.EndTime
	}
	if in.StartFromHead != nil && !*in.StartFromHead {
		q.cur = cursor{dir: backward, at: position{ts: math.MaxInt64}}
	}
	if in.NextToken != nil {
		parsed, ok := parseCursor(*in.NextToken)
		if !ok {
			return filterQuery{}, errInvalidToken()
		}
		q.cur = parsed
	}
	if in.Limit != nil {
		q.limit = int(*in.Limit)
	}
	return q, nil
}

// matches reports whether an event is in the time range and matches the
// pattern.
func (q *filterQuery) matches(e *Event) bool {
	return e.Timestamp >= q.lo && e.Timestamp <= q.hi && logsfilter.Match(q.filter, e.Message)
}

// selects reports whether the request's stream selection includes name.
func selects(in *logsapi.FilterLogEventsInput, name string) bool {
	switch {
	case len(in.LogStreamNames) > 0:
		return slices.Contains(in.LogStreamNames, name)
	case in.LogStreamNamePrefix != nil:
		return strings.HasPrefix(name, *in.LogStreamNamePrefix)
	}
	return true
}

// filterPage cuts one page from matches in position order: forward pages
// run oldest first from the cursor, backward pages newest first before it.
// The next cursor is nil when no match remains past the page.
func filterPage(matches []filtered, cur cursor, limit int) ([]filtered, *cursor) {
	at := func(p position) int {
		i, _ := slices.BinarySearchFunc(matches, p, func(m filtered, p position) int { return m.event.pos().compare(p) })
		return i
	}
	var page []filtered
	size := 0
	switch cur.dir {
	case forward:
		i := at(cur.at)
		for ; i < len(matches) && len(page) < limit && fits(&size, &matches[i].event); i++ {
			page = append(page, matches[i])
		}
		if i < len(matches) {
			return page, &cursor{dir: forward, at: matches[i-1].event.pos().next()}
		}
	case backward:
		i := at(cur.at) - 1
		for ; i >= 0 && len(page) < limit && fits(&size, &matches[i].event); i-- {
			page = append(page, matches[i])
		}
		if i >= 0 {
			return page, &cursor{dir: backward, at: matches[i+1].event.pos()}
		}
	}
	return page, nil
}
