"""CloudWatch alarm evaluation and GetMetricStatistics (cloudwatch-alarms.md).

The arbiter is docs/research/cloudwatch-alarms.md; each assertion cites the
section that records or documents the value.
"""

from __future__ import annotations

import datetime as dt
import json
from collections.abc import Callable
from typing import Any

import pytest

from support import Admin, raises, status_of, uniq


@pytest.fixture(scope="session")
def cloudwatch(make_client: Callable[..., Any]) -> Any:
    return make_client("cloudwatch")


@pytest.fixture
def make_alarm(cloudwatch: Any, cleanup: Any) -> Callable[..., str]:
    """Create a Sum-over-60s alarm on Conformance/<metric>, breaching above 10."""

    def make(metric: str, **extra: Any) -> str:
        name = uniq("conf-alarm")
        cloudwatch.put_metric_alarm(
            AlarmName=name,
            Namespace="Conformance",
            MetricName=metric,
            Statistic="Sum",
            Period=60,
            EvaluationPeriods=1,
            Threshold=10.0,
            ComparisonOperator="GreaterThanThreshold",
            **extra,
        )
        cleanup.add(cloudwatch.delete_alarms, AlarmNames=[name])
        return name

    return make


def _alarm(cloudwatch: Any, name: str) -> dict[str, Any]:
    alarms = cloudwatch.describe_alarms(AlarmNames=[name])["MetricAlarms"]
    assert len(alarms) == 1, alarms
    return alarms[0]


def test_clock_advance_evaluates_an_alarm(cloudwatch: Any, make_alarm: Callable[..., str], frozen_clock: Admin) -> None:
    # Data put without a Timestamp is stamped with the emulator clock, so it
    # lines up with the alarm's windows (cwa 12.3 item 5).
    metric = uniq("load")
    cloudwatch.put_metric_data(Namespace="Conformance", MetricData=[{"MetricName": metric, "Value": 20.0}])
    name = make_alarm(metric)

    # No synchronous evaluation: right after the create the alarm still has
    # the initial reason (cwa 3.1, third-party-recorded).
    created = _alarm(cloudwatch, name)
    assert created["StateValue"] == "INSUFFICIENT_DATA"
    assert created["StateReason"] == "Unchecked: Initial alarm creation"

    frozen_clock.advance(60)
    breached = _alarm(cloudwatch, name)
    assert breached["StateValue"] == "ALARM"
    # Count form, values as Java doubles, dates dd/MM/yy (cwa 4.1, 4.3).
    assert breached["StateReason"].startswith("Threshold Crossed: 1 datapoint [20.0 (")
    assert breached["StateReason"].endswith(")] was greater than the threshold (10.0).")
    data = json.loads(breached["StateReasonData"])
    # cwa 4.2: version, statistic, period, the used values and the threshold.
    assert data["version"] == "1.0"
    assert data["statistic"] == "Sum"
    assert data["period"] == 60
    assert data["recentDatapoints"] == [20.0]
    assert data["threshold"] == 10.0
    # Evaluation never writes the configuration Terraform reads (cwa 12.6).
    assert breached["AlarmConfigurationUpdatedTimestamp"] == created["AlarmConfigurationUpdatedTimestamp"]

    history = cloudwatch.describe_alarm_history(AlarmName=name)["AlarmHistoryItems"]
    # cwa 8.2, third-party-recorded.
    assert [h["HistorySummary"] for h in history] == ["Alarm updated from INSUFFICIENT_DATA to ALARM"]
    assert history[0]["HistoryItemType"] == "StateUpdate"
    assert history[0]["AlarmType"] == "MetricAlarm"
    new_state = json.loads(history[0]["HistoryData"])["newState"]
    assert new_state["stateValue"] == "ALARM"
    assert new_state["stateReasonData"]["recentDatapoints"] == [20.0]

    # Once the point leaves the evaluation range every bin is missing, and
    # the default missing treatment goes to INSUFFICIENT_DATA (cwa 3.5).
    frozen_clock.advance(600)
    assert _alarm(cloudwatch, name)["StateValue"] == "INSUFFICIENT_DATA"
    history = cloudwatch.describe_alarm_history(AlarmName=name)["AlarmHistoryItems"]
    assert [h["HistorySummary"] for h in history] == [
        "Alarm updated from ALARM to INSUFFICIENT_DATA",
        "Alarm updated from INSUFFICIENT_DATA to ALARM",
    ]


def test_set_alarm_state_lasts_until_the_next_evaluation(
    cloudwatch: Any, make_alarm: Callable[..., str], frozen_clock: Admin
) -> None:
    # "This temporary state change lasts only until the next alarm
    # comparison occurs" (cwa 2.3, aws-doc); ignore with no data retains it,
    # as AWS recorded (cwa 2.3, third-party-recorded).
    missing = make_alarm(uniq("quiet"))
    ignore = make_alarm(uniq("quiet"), TreatMissingData="ignore")
    for name in (missing, ignore):
        cloudwatch.set_alarm_state(AlarmName=name, StateValue="ALARM", StateReason="testing alarm")
        assert _alarm(cloudwatch, name)["StateValue"] == "ALARM"
    frozen_clock.advance(60)
    assert _alarm(cloudwatch, missing)["StateValue"] == "INSUFFICIENT_DATA"
    kept = _alarm(cloudwatch, ignore)
    assert kept["StateValue"] == "ALARM"
    assert kept["StateReason"] == "testing alarm"


def test_describe_alarms_for_metric(cloudwatch: Any, make_alarm: Callable[..., str]) -> None:
    # Exact metric and dimension set; the answer has MetricAlarms only
    # (cwa 9.4, third-party-recorded).
    metric = uniq("load")
    dims = [{"Name": "A", "Value": "1"}, {"Name": "B", "Value": "2"}]
    name = make_alarm(metric, Dimensions=dims, TreatMissingData="ignore")
    make_alarm(metric, Dimensions=dims[:1], TreatMissingData="ignore")
    out = cloudwatch.describe_alarms_for_metric(Namespace="Conformance", MetricName=metric, Dimensions=list(reversed(dims)))
    assert [a["AlarmName"] for a in out["MetricAlarms"]] == [name]
    assert "CompositeAlarms" not in out
    none = cloudwatch.describe_alarms_for_metric(Namespace="Conformance", MetricName=uniq("nothing"))
    assert none["MetricAlarms"] == []


def test_get_metric_statistics(cloudwatch: Any, frozen_clock: Admin) -> None:
    metric = uniq("stats")
    cloudwatch.put_metric_data(
        Namespace="Conformance", MetricData=[{"MetricName": metric, "Value": float(v)} for v in range(10)]
    )
    whole = frozen_clock.clock()["now"].split(".")[0].removesuffix("Z")
    start = dt.datetime.fromisoformat(whole).replace(second=0, tzinfo=dt.UTC)
    out = cloudwatch.get_metric_statistics(
        Namespace="Conformance",
        MetricName=metric,
        StartTime=start,
        EndTime=start + dt.timedelta(minutes=1),
        Period=60,
        Statistics=["SampleCount", "Average", "Sum", "Minimum", "Maximum"],
    )
    # Values 0 to 9 in one minute, recorded (cwa 9.2).
    assert out["Label"] == metric
    assert len(out["Datapoints"]) == 1
    point = out["Datapoints"][0]
    assert {k: point[k] for k in ("SampleCount", "Average", "Sum", "Minimum", "Maximum", "Unit")} == {
        "SampleCount": 10.0,
        "Average": 4.5,
        "Sum": 45.0,
        "Minimum": 0.0,
        "Maximum": 9.0,
        "Unit": "None",
    }
    assert point["Timestamp"] == start


def test_get_metric_statistics_limits(cloudwatch: Any) -> None:
    now = dt.datetime.now(dt.UTC)
    # cwa 9.3, third-party-recorded: the 1440 limit before any data is read,
    # and StartTime equal to EndTime.
    with raises("InvalidParameterCombination") as info:
        cloudwatch.get_metric_statistics(
            Namespace="Conformance",
            MetricName="metric_name",
            StartTime=now,
            EndTime=now + dt.timedelta(days=1),
            Period=1,
            Statistics=["Average"],
        )
    assert status_of(info.value) == 400
    assert info.value.response["Error"]["Message"] == (
        "You have requested up to 86400 datapoints, which exceeds the limit of 1440. "
        "You may reduce the datapoints requested by increasing Period, or decreasing the time range."
    )
    with raises("InvalidParameterValue") as info:
        cloudwatch.get_metric_statistics(
            Namespace="Conformance", MetricName="metric_name", StartTime=now, EndTime=now, Period=60, Statistics=["Average"]
        )
    assert info.value.response["Error"]["Message"] == "The parameter StartTime must be less than the parameter EndTime."
    empty = cloudwatch.get_metric_statistics(
        Namespace="Conformance",
        MetricName="metric_name",
        StartTime=now,
        EndTime=now + dt.timedelta(days=1),
        Period=60,
        Statistics=["Average"],
    )
    assert empty["Datapoints"] == []
    assert empty["Label"] == "metric_name"
