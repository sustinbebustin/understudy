#!/usr/bin/env bash
# CloudWatch metrics: metric alarms (CRUD, state, tags, actions) and
# PutMetricData / GetMetricData / ListMetrics.
# Arbiter: docs/research/phase1/secrets-logs-events-alarms.md section 5.

# new_alarm [PUT-METRIC-ALARM ARGS...]: create a minimal static-threshold alarm
# named in ALARM (ARN in ALARM_ARN). Extra arguments are appended.
new_alarm() {
  ALARM=$(new_name cw-alarm)
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
  ALARM_ARN="arn:aws:cloudwatch:${AWS_REGION}:${ACCT}:alarm:${ALARM}"
  aws_ok cloudwatch put-metric-alarm --alarm-name "$ALARM" --namespace Conformance --metric-name CPUUtilization \
    --statistic Average --period 60 --evaluation-periods 1 --threshold 80 --comparison-operator GreaterThanThreshold "$@"
}

# describe_alarm NAME: DescribeAlarms for one alarm; the alarm is in AWS_OUT as .MetricAlarms[0].
describe_alarm() {
  aws_ok cloudwatch describe-alarms --alarm-names "$1"
}

# ---------------------------------------------------------------------------
# PutMetricAlarm, DescribeAlarms, DeleteAlarms
# ---------------------------------------------------------------------------

test_put_metric_alarm_minimal() {
  new_alarm
  assert_eq 200 "$AWS_STATUS" 'PutMetricAlarm status'
  assert_eq '' "$AWS_OUT" 'PutMetricAlarm response body'
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms | length' '1'
  assert_jq '.MetricAlarms[0].AlarmName' "$ALARM"
  assert_jq '.MetricAlarms[0].AlarmArn' "$ALARM_ARN"
  assert_jq '.MetricAlarms[0].Namespace' 'Conformance'
  assert_jq '.MetricAlarms[0].MetricName' 'CPUUtilization'
  assert_jq '.MetricAlarms[0].Statistic' 'Average'
  assert_jq '.MetricAlarms[0].Period' '60'
  assert_jq '.MetricAlarms[0].EvaluationPeriods' '1'
  assert_jq '.MetricAlarms[0].Threshold' '80'
  assert_jq '.MetricAlarms[0].ComparisonOperator' 'GreaterThanThreshold'
  assert_jq '.MetricAlarms[0].ActionsEnabled' 'true'
  assert_jq '.MetricAlarms[0].StateValue' 'INSUFFICIENT_DATA'
  assert_jq '.MetricAlarms[0].StateReason' 'Unchecked: Initial alarm creation'
  assert_jq_true '.MetricAlarms[0] | .AlarmActions == [] and .OKActions == [] and .InsufficientDataActions == [] and .Dimensions == []'
  assert_jq_match '.MetricAlarms[0].AlarmConfigurationUpdatedTimestamp' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  assert_jq_match '.MetricAlarms[0].StateUpdatedTimestamp' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  assert_jq_true '.MetricAlarms[0] | has("AlarmDescription") | not'
}

test_put_metric_alarm_all_settings() {
  new_alarm --alarm-description 'cpu too high' --dimensions Name=InstanceId,Value=i-123 Name=Role,Value=web \
    --alarm-actions "arn:aws:sns:${AWS_REGION}:111111111111:alerts" --ok-actions "arn:aws:sns:${AWS_REGION}:111111111111:recovered" \
    --insufficient-data-actions "arn:aws:sns:${AWS_REGION}:111111111111:nodata" --treat-missing-data notBreaching \
    --datapoints-to-alarm 2 --evaluation-periods 3 --unit Percent --no-actions-enabled
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].AlarmDescription' 'cpu too high'
  assert_jq_true '[.MetricAlarms[0].Dimensions[] | {(.Name): .Value}] | add == {"InstanceId":"i-123","Role":"web"}'
  assert_jq_true ".MetricAlarms[0].AlarmActions == [\"arn:aws:sns:${AWS_REGION}:111111111111:alerts\"]"
  assert_jq_true ".MetricAlarms[0].OKActions == [\"arn:aws:sns:${AWS_REGION}:111111111111:recovered\"]"
  assert_jq_true ".MetricAlarms[0].InsufficientDataActions == [\"arn:aws:sns:${AWS_REGION}:111111111111:nodata\"]"
  assert_jq '.MetricAlarms[0].TreatMissingData' 'notBreaching'
  assert_jq '.MetricAlarms[0].DatapointsToAlarm' '2'
  assert_jq '.MetricAlarms[0].EvaluationPeriods' '3'
  assert_jq '.MetricAlarms[0].Unit' 'Percent'
  assert_jq '.MetricAlarms[0].ActionsEnabled' 'false'
}

test_put_metric_alarm_extended_statistic() {
  ALARM=$(new_name cw-alarm)
  aws_ok cloudwatch put-metric-alarm --alarm-name "$ALARM" --namespace Conformance --metric-name Latency \
    --extended-statistic p99 --period 300 --evaluation-periods 2 --threshold 1.5 --comparison-operator GreaterThanOrEqualToThreshold
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].ExtendedStatistic' 'p99'
  assert_jq '.MetricAlarms[0] | has("Statistic")' 'false'
  assert_jq '.MetricAlarms[0].Threshold' '1.5'
}

test_put_metric_alarm_again_overwrites_configuration_and_keeps_state() {
  new_alarm --alarm-description first
  aws_ok cloudwatch set-alarm-state --alarm-name "$ALARM" --state-value ALARM --state-reason 'forced for test'
  aws_ok cloudwatch put-metric-alarm --alarm-name "$ALARM" --namespace Conformance --metric-name CPUUtilization \
    --statistic Maximum --period 120 --evaluation-periods 2 --threshold 95 --comparison-operator GreaterThanThreshold
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].Statistic' 'Maximum'
  assert_jq '.MetricAlarms[0].Period' '120'
  assert_jq '.MetricAlarms[0].Threshold' '95'
  assert_jq '.MetricAlarms[0] | has("AlarmDescription")' 'false'
  assert_jq '.MetricAlarms[0].StateValue' 'ALARM'
}

test_describe_alarms_unknown_name_is_empty() {
  aws_ok cloudwatch describe-alarms --alarm-names "$(new_name cw-missing)"
  assert_jq_true '.MetricAlarms == []'
}

test_describe_alarms_name_prefix() {
  local prefix
  prefix=$(new_name cw-prefix)
  local n
  for n in a b c; do
    aws_ok cloudwatch put-metric-alarm --alarm-name "${prefix}-${n}" --namespace Conformance --metric-name M \
      --statistic Sum --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator LessThanThreshold
  done
  aws_ok cloudwatch describe-alarms --alarm-name-prefix "$prefix"
  assert_jq_true '[.MetricAlarms[].AlarmName] | sort == ["'"$prefix"'-a","'"$prefix"'-b","'"$prefix"'-c"]'
}

test_describe_alarms_by_state_value() {
  local prefix
  prefix=$(new_name cw-state)
  aws_ok cloudwatch put-metric-alarm --alarm-name "${prefix}-x" --namespace Conformance --metric-name M \
    --statistic Sum --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator LessThanThreshold
  aws_ok cloudwatch put-metric-alarm --alarm-name "${prefix}-y" --namespace Conformance --metric-name M \
    --statistic Sum --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator LessThanThreshold
  aws_ok cloudwatch set-alarm-state --alarm-name "${prefix}-y" --state-value OK --state-reason done
  aws_ok cloudwatch describe-alarms --alarm-name-prefix "$prefix" --state-value OK
  assert_jq_true '[.MetricAlarms[].AlarmName] == ["'"$prefix"'-y"]'
}

test_describe_alarms_returns_metric_alarms_by_default() {
  new_alarm
  aws_ok cloudwatch describe-alarms --alarm-names "$ALARM"
  assert_jq_true '.CompositeAlarms == []'
}

test_set_alarm_state() {
  new_alarm
  aws_ok cloudwatch set-alarm-state --alarm-name "$ALARM" --state-value ALARM --state-reason 'manual reason'
  assert_eq 200 "$AWS_STATUS" 'SetAlarmState status'
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].StateValue' 'ALARM'
  assert_jq '.MetricAlarms[0].StateReason' 'manual reason'
}

test_set_alarm_state_invalid_value() {
  new_alarm
  aws_error ValidationError 400 cloudwatch set-alarm-state --alarm-name "$ALARM" --state-value INVALID --state-reason x
  aws_error_message_contains "Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]"
}

test_set_alarm_state_missing_alarm() {
  aws_error ResourceNotFound 404 cloudwatch set-alarm-state --alarm-name "$(new_name cw-missing)" --state-value OK --state-reason x
}

test_delete_alarms() {
  new_alarm
  aws_ok cloudwatch delete-alarms --alarm-names "$ALARM"
  assert_eq 200 "$AWS_STATUS" 'DeleteAlarms status'
  describe_alarm "$ALARM"
  assert_jq_true '.MetricAlarms == []'
}

test_delete_alarms_skips_names_that_do_not_exist() {
  new_alarm
  local other=$ALARM
  new_alarm
  aws_ok cloudwatch delete-alarms --alarm-names "$other" "$(new_name cw-missing)" "$ALARM"
  aws_ok cloudwatch describe-alarms --alarm-names "$other" "$ALARM"
  assert_jq_true '.MetricAlarms == []'
}

test_enable_and_disable_alarm_actions() {
  new_alarm
  aws_ok cloudwatch disable-alarm-actions --alarm-names "$ALARM"
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].ActionsEnabled' 'false'
  aws_ok cloudwatch enable-alarm-actions --alarm-names "$ALARM"
  describe_alarm "$ALARM"
  assert_jq '.MetricAlarms[0].ActionsEnabled' 'true'
}

# ---------------------------------------------------------------------------
# Tags
# ---------------------------------------------------------------------------

test_alarm_tags_at_creation() {
  new_alarm --tags Key=env,Value=test Key=team,Value=core
  aws_ok cloudwatch list-tags-for-resource --resource-arn "$ALARM_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
}

test_tag_and_untag_alarm() {
  new_alarm
  aws_ok cloudwatch tag-resource --resource-arn "$ALARM_ARN" --tags Key=a,Value=1 Key=b,Value=2
  aws_ok cloudwatch untag-resource --resource-arn "$ALARM_ARN" --tag-keys a
  aws_ok cloudwatch list-tags-for-resource --resource-arn "$ALARM_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"b":"2"}'
}

test_tags_on_put_again_are_ignored() {
  new_alarm --tags Key=env,Value=first
  aws_ok cloudwatch put-metric-alarm --alarm-name "$ALARM" --namespace Conformance --metric-name CPUUtilization \
    --statistic Average --period 60 --evaluation-periods 1 --threshold 80 --comparison-operator GreaterThanThreshold \
    --tags Key=env,Value=second
  aws_ok cloudwatch list-tags-for-resource --resource-arn "$ALARM_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"first"}'
}

test_list_tags_for_deleted_alarm_is_empty() {
  new_alarm --tags Key=env,Value=test
  aws_ok cloudwatch delete-alarms --alarm-names "$ALARM"
  aws_ok cloudwatch list-tags-for-resource --resource-arn "$ALARM_ARN"
  assert_jq_true '.Tags == []'
}

# ---------------------------------------------------------------------------
# PutMetricData, GetMetricData, ListMetrics
# ---------------------------------------------------------------------------

# iso EPOCH-SECONDS: an RFC 3339 UTC timestamp.
iso() {
  date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ
}

# new_namespace: a namespace for one test; sets NS.
new_namespace() {
  NS="Conformance/$(new_name metrics)"
}

# put_samples METRIC VALUE...: put each value as its own datum in the same
# minute, ten minutes ago. Sets SAMPLE_MINUTE (epoch seconds of that minute).
put_samples() {
  local metric=$1
  shift
  SAMPLE_MINUTE=$(( ($(date +%s) / 60) * 60 - 600 ))
  local data="[" v
  for v in "$@"; do
    data+="{\"MetricName\":\"$metric\",\"Value\":$v,\"Timestamp\":\"$(iso $((SAMPLE_MINUTE + 10)))\",\"Unit\":\"Count\"},"
  done
  aws_ok cloudwatch put-metric-data --namespace "$NS" --metric-data "${data%,}]"
}

# stat METRIC STAT: the value GetMetricData reports for the sample minute.
stat() {
  local queries
  queries=$(jq -nc --arg ns "$NS" --arg m "$1" --arg s "$2" \
    '[{Id:"q1",MetricStat:{Metric:{Namespace:$ns,MetricName:$m},Period:60,Stat:$s}}]')
  aws_ok cloudwatch get-metric-data --metric-data-queries "$queries" \
    --start-time "$(iso $((SAMPLE_MINUTE - 300)))" --end-time "$(iso $((SAMPLE_MINUTE + 300)))"
}

test_put_metric_data_response_is_empty() {
  new_namespace
  aws_ok cloudwatch put-metric-data --namespace "$NS" --metric-name Visits --value 1
  assert_eq 200 "$AWS_STATUS" 'PutMetricData status'
  assert_eq '' "$AWS_OUT" 'PutMetricData response body'
}

test_get_metric_data_statistics() {
  new_namespace
  put_samples Items 1 2 3 6
  stat Items Sum
  assert_jq '.MetricDataResults[0].Id' 'q1'
  assert_jq '.MetricDataResults[0].StatusCode' 'Complete'
  assert_jq_true '.MetricDataResults[0].Values == [12.0]'
  assert_jq '.MetricDataResults[0].Timestamps | length' '1'
  stat Items Average
  assert_jq_true '.MetricDataResults[0].Values == [3.0]'
  stat Items Minimum
  assert_jq_true '.MetricDataResults[0].Values == [1.0]'
  stat Items Maximum
  assert_jq_true '.MetricDataResults[0].Values == [6.0]'
  stat Items SampleCount
  assert_jq_true '.MetricDataResults[0].Values == [4.0]'
}

test_get_metric_data_timestamp_is_the_period_start() {
  new_namespace
  put_samples Items 5
  stat Items Sum
  assert_eq "$(iso "$SAMPLE_MINUTE")" "$(jqv '.MetricDataResults[0].Timestamps[0]' | sed -E 's/\+00:00$/Z/')" 'datapoint timestamp'
}

test_get_metric_data_other_metric_has_no_data() {
  new_namespace
  put_samples Items 5
  stat Other Sum
  assert_jq '.MetricDataResults[0].StatusCode' 'Complete'
  assert_jq_true '.MetricDataResults[0].Values == []'
}

test_get_metric_data_dimensions_select_series() {
  new_namespace
  local ts
  ts=$(iso $(( ($(date +%s) / 60) * 60 - 600 + 5 )))
  aws_ok cloudwatch put-metric-data --namespace "$NS" --metric-data "[
    {\"MetricName\":\"Load\",\"Dimensions\":[{\"Name\":\"Host\",\"Value\":\"a\"}],\"Value\":10,\"Timestamp\":\"$ts\"},
    {\"MetricName\":\"Load\",\"Dimensions\":[{\"Name\":\"Host\",\"Value\":\"b\"}],\"Value\":20,\"Timestamp\":\"$ts\"}]"
  SAMPLE_MINUTE=$(( ($(date +%s) / 60) * 60 - 600 ))
  local queries
  queries=$(jq -nc --arg ns "$NS" \
    '[{Id:"a",MetricStat:{Metric:{Namespace:$ns,MetricName:"Load",Dimensions:[{Name:"Host",Value:"a"}]},Period:60,Stat:"Sum"}},
      {Id:"b",MetricStat:{Metric:{Namespace:$ns,MetricName:"Load",Dimensions:[{Name:"Host",Value:"b"}]},Period:60,Stat:"Sum"}}]')
  aws_ok cloudwatch get-metric-data --metric-data-queries "$queries" \
    --start-time "$(iso $((SAMPLE_MINUTE - 300)))" --end-time "$(iso $((SAMPLE_MINUTE + 300)))"
  assert_jq_true '[.MetricDataResults[] | {(.Id): .Values}] | add == {"a":[10.0],"b":[20.0]}'
}

test_put_metric_data_values_and_counts() {
  new_namespace
  SAMPLE_MINUTE=$(( ($(date +%s) / 60) * 60 - 600 ))
  aws_ok cloudwatch put-metric-data --namespace "$NS" --metric-data "[{\"MetricName\":\"Hist\",\"Values\":[1,5],\"Counts\":[3,2],\"Timestamp\":\"$(iso $((SAMPLE_MINUTE + 1)))\"}]"
  stat Hist SampleCount
  assert_jq_true '.MetricDataResults[0].Values == [5.0]'
  stat Hist Sum
  assert_jq_true '.MetricDataResults[0].Values == [13.0]'
}

test_put_metric_data_statistic_values() {
  new_namespace
  SAMPLE_MINUTE=$(( ($(date +%s) / 60) * 60 - 600 ))
  aws_ok cloudwatch put-metric-data --namespace "$NS" --metric-name Agg --timestamp "$(iso $((SAMPLE_MINUTE + 1)))" \
    --statistic-values Sum=30,Minimum=2,Maximum=20,SampleCount=3
  stat Agg Maximum
  assert_jq_true '.MetricDataResults[0].Values == [20.0]'
  stat Agg Sum
  assert_jq_true '.MetricDataResults[0].Values == [30.0]'
}

test_put_metric_data_value_and_values_are_exclusive() {
  new_namespace
  aws_error InvalidParameterCombination 400 cloudwatch put-metric-data --namespace "$NS" \
    --metric-data '[{"MetricName":"M","Value":1,"Values":[1],"Counts":[1]}]'
  aws_error_message_contains 'The parameters MetricData.member.1.Value and MetricData.member.1.Values are mutually exclusive and you have specified both.'
}

test_put_metric_data_values_and_counts_must_match() {
  new_namespace
  aws_error InvalidParameterValue 400 cloudwatch put-metric-data --namespace "$NS" \
    --metric-data '[{"MetricName":"M","Values":[1,2,3],"Counts":[1]}]'
  aws_error_message_contains 'The parameters MetricData.member.1.Values and MetricData.member.1.Counts must be of the same size.'
}

test_list_metrics() {
  new_namespace
  put_samples Items 1
  aws_ok cloudwatch list-metrics --namespace "$NS"
  assert_jq_true '[.Metrics[] | {Namespace, MetricName}] == [{"Namespace":"'"$NS"'","MetricName":"Items"}]'
}
