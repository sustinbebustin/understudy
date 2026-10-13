package cloudwatch

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots.
const stateVersion = 1

// State is the CloudWatch state of one account and region: its metric
// alarms by name and its metric series by key. Records are values replaced
// on change, never written in place, so a value read out of a cell never
// aliases a later write.
type State struct {
	Alarms map[string]Alarm  `json:"alarms"`
	Series map[string]Series `json:"series"`
}

func newState(aws.Scope) State {
	return State{Alarms: map[string]Alarm{}, Series: map[string]Series{}}
}

// IsZero reports a scope without alarms or metrics, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Alarms) == 0 && len(s.Series) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Alarm is one metric alarm.
type Alarm struct {
	// Config is the PutMetricAlarm request that last set the alarm, without
	// its Tags and ActionsEnabled: an update ignores tags, and
	// EnableAlarmActions and DisableAlarmActions change ActionsEnabled
	// alone. DescribeAlarms echoes it.
	Config         cloudwatchapi.PutMetricAlarmInput `json:"config"`
	ActionsEnabled bool                              `json:"actionsEnabled"`
	ConfigUpdated  clock.Instant                     `json:"configUpdated"`
	State          AlarmState                        `json:"state"`
	Tags           map[string]string                 `json:"tags,omitempty"`
}

// AlarmState is an alarm's state as PutMetricAlarm created it or
// SetAlarmState last set it; understudy never evaluates an alarm.
type AlarmState struct {
	Value      cloudwatchapi.StateValue `json:"value"`
	Reason     string                   `json:"reason"`
	ReasonData *string                  `json:"reasonData,omitempty"`
	// Updated is the last state change or SetAlarmState; Transitioned the
	// last change of Value.
	Updated      clock.Instant `json:"updated"`
	Transitioned clock.Instant `json:"transitioned"`
}

// Series is one metric: a namespace, a name and a dimension set, with the
// data points PutMetricData stored for it in arrival order.
type Series struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Dimensions are sorted by name, so a dimension set names one series
	// whatever order a request lists it in.
	Dimensions []Dimension `json:"dimensions,omitempty"`
	Points     []Point     `json:"points,omitempty"`
	// LastPut is when PutMetricData last stored a point, which ListMetrics
	// reads for its two-week span and RecentlyActive.
	LastPut clock.Instant `json:"lastPut"`
}

// Dimension is one name and value of a series.
type Dimension struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Point is one stored datum: its timestamp at its storage resolution, its
// unit and the statistic set it carries. A Value is the set {1, v, v}; Values and Counts fold
// into one set.
type Point struct {
	At    clock.Instant `json:"at"`
	Unit  string        `json:"unit"`
	Stats Stats         `json:"stats"`
}

// Stats is a statistic set; points of one period combine into one.
type Stats struct {
	SampleCount float64 `json:"sampleCount"`
	Sum         float64 `json:"sum"`
	Minimum     float64 `json:"minimum"`
	Maximum     float64 `json:"maximum"`
}

// add combines two statistic sets.
func (s Stats) add(o Stats) Stats {
	return Stats{
		SampleCount: s.SampleCount + o.SampleCount,
		Sum:         s.Sum + o.Sum,
		Minimum:     min(s.Minimum, o.Minimum),
		Maximum:     max(s.Maximum, o.Maximum),
	}
}

// seriesKey is the key of the series a namespace, name and sorted
// dimension set name: each part quoted, so no name can forge a separator.
func seriesKey(namespace, name string, dims []Dimension) string {
	var b strings.Builder
	b.WriteString(strconv.Quote(namespace))
	b.WriteString(strconv.Quote(name))
	for _, d := range dims {
		b.WriteString(strconv.Quote(d.Name))
		b.WriteString(strconv.Quote(d.Value))
	}
	return b.String()
}

// sortedDimensions is a request's dimension list as a series holds it.
func sortedDimensions(in []cloudwatchapi.Dimension) []Dimension {
	if len(in) == 0 {
		return nil
	}
	out := make([]Dimension, len(in))
	for i, d := range in {
		out[i] = Dimension{Name: d.Name, Value: d.Value}
	}
	slices.SortFunc(out, func(a, b Dimension) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Value, b.Value)
	})
	return out
}

// wireDimensions is a series' dimension list on the wire: always a list,
// empty when the series has none.
func wireDimensions(dims []Dimension) []cloudwatchapi.Dimension {
	out := make([]cloudwatchapi.Dimension, len(dims))
	for i, d := range dims {
		out[i] = cloudwatchapi.Dimension{Name: d.Name, Value: d.Value}
	}
	return out
}

// stateCodec returns the state codec, with the two unions an alarm's
// configuration may carry in the tagged-union convention. The tables are
// static, so an error is a bug in them, reported by every Export and Import.
func stateCodec() store.Codec {
	window, err := jsonx.NewUnion("cloudwatch.EvaluationWindow",
		jsonx.Variant[cloudwatchapi.EvaluationWindow, cloudwatchapi.EvaluationWindowMemberWallClockWindow]("wall-clock"),
		jsonx.Variant[cloudwatchapi.EvaluationWindow, cloudwatchapi.EvaluationWindowMemberSlidingWindow]("sliding"),
	)
	if err != nil {
		return failingCodec(err)
	}
	criteria, err := jsonx.NewUnion("cloudwatch.EvaluationCriteria",
		jsonx.Variant[cloudwatchapi.EvaluationCriteria, cloudwatchapi.EvaluationCriteriaMemberPromQLCriteria]("promql"),
	)
	if err != nil {
		return failingCodec(err)
	}
	c := jsonx.NewCodec(window, criteria)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

func failingCodec(err error) store.Codec {
	return store.Codec{
		Marshal:   func(any) ([]byte, error) { return nil, err },
		Unmarshal: func([]byte, any) error { return err },
	}
}
