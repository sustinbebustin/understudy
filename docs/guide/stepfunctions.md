# Step Functions

understudy runs Standard workflows written in JSONPath: Pass, Task, Choice, Wait, Succeed, Fail, Parallel and inline Map states, with Retry and Catch, the context object and the intrinsic functions. A Task calls a Lambda function, by its ARN or through `arn:aws:states:::lambda:invoke`; running one needs Docker, as Lambda does ([lambda.md](lambda.md)). Every step runs on the emulator clock, so a Wait of an hour finishes when you advance the clock an hour ([clock.md](clock.md)). What each operation supports is in [coverage.md](../coverage.md); `understudy coverage --service sfn` prints it from the binary.

The commands below use the AWS CLI with `eval "$(understudy env)"` ([getting-started.md](getting-started.md)), account `000000000000` and `us-east-1`.

## A machine, an execution and its history

```sh
cat > wait.json <<'EOF'
{
  "StartAt": "Greet",
  "States": {
    "Greet": {"Type": "Pass", "Result": {"greeting": "hello"}, "ResultPath": "$.pass", "Next": "Pause"},
    "Pause": {"Type": "Wait", "Seconds": 3600, "Next": "Done"},
    "Done": {"Type": "Succeed"}
  }
}
EOF
aws stepfunctions validate-state-machine-definition --definition file://wait.json
# "result": "OK", "diagnostics": [], "truncated": false
aws stepfunctions create-state-machine --name waiter --role-arn arn:aws:iam::000000000000:role/sfn-role \
  --definition file://wait.json
# "stateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:waiter"
aws stepfunctions start-execution --state-machine-arn arn:aws:states:us-east-1:000000000000:stateMachine:waiter \
  --name first --input '{"order":42}'
# "executionArn": "arn:aws:states:us-east-1:000000000000:execution:waiter:first"
aws stepfunctions describe-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:waiter:first \
  --query status
# "RUNNING"
```

The execution is now in its Wait. Advance the clock past it; `--settle` returns once the steps it released have run:

```sh
understudy clock advance 1h --settle
# fired 2 timer(s):
#   sfn.step arn:aws:states:us-east-1:000000000000:execution:waiter:first/4
#   sfn.step arn:aws:states:us-east-1:000000000000:execution:waiter:first/5
# settled: no tracked work is pending
aws stepfunctions describe-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:waiter:first \
  --query '{status:status,output:output}'
# "status": "SUCCEEDED",
# "output": "{\"order\":42,\"pass\":{\"greeting\":\"hello\"}}"
aws stepfunctions get-execution-history --execution-arn arn:aws:states:us-east-1:000000000000:execution:waiter:first \
  --query 'events[].[id,previousEventId,type]' --output text
# 1  0  ExecutionStarted
# 2  0  PassStateEntered
# 3  2  PassStateExited
# 4  3  WaitStateEntered
# 5  4  WaitStateExited
# 6  5  SucceedStateEntered
# 7  6  SucceedStateExited
# 8  7  ExecutionSucceeded
```

History events carry the ids, links and details AWS writes; `get-execution-history` pages, takes `--reverse-order` and `--no-include-execution-data`. A machine's `TimeoutSeconds` and the one-year execution limit run on the same clock.

`stop-execution` ends a running execution `ABORTED`, keeping the error and cause, and `list-executions` lists the newest first:

```sh
aws stepfunctions start-execution --state-machine-arn arn:aws:states:us-east-1:000000000000:stateMachine:waiter --name second
aws stepfunctions stop-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:waiter:second \
  --error Halt --cause 'by hand'
aws stepfunctions describe-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:waiter:second \
  --query '{status:status,error:error,cause:cause}'
# "status": "ABORTED", "error": "Halt", "cause": "by hand"
aws stepfunctions list-executions --state-machine-arn arn:aws:states:us-east-1:000000000000:stateMachine:waiter \
  --query 'executions[].[name,status]' --output text
# second  ABORTED
# first   SUCCEEDED
```

A closed execution is kept for 90 days of emulator time, as on AWS. Deleting a machine with running executions leaves it `DELETING` until they end, and each fails at its next step, as on AWS.

## A Lambda task

A Task calls the function as the machine's execution role. The function runs in a container, so after `start-execution` wait with `understudy settle` (or `clock advance --settle`) before reading the result:

```sh
cat > handler.py <<'EOF'
def handler(event, context):
    return {"total": event["a"] + event["b"]}
EOF
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name adder --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip
cat > task.json <<'EOF'
{
  "StartAt": "Add",
  "States": {
    "Add": {
      "Type": "Task",
      "Resource": "arn:aws:states:::lambda:invoke",
      "Parameters": {"FunctionName": "arn:aws:lambda:us-east-1:000000000000:function:adder", "Payload.$": "$"},
      "ResultSelector": {"total.$": "$.Payload.total"},
      "Retry": [{"ErrorEquals": ["Lambda.ServiceException", "Lambda.TooManyRequestsException"], "MaxAttempts": 2}],
      "Catch": [{"ErrorEquals": ["States.ALL"], "ResultPath": "$.error", "Next": "Failed"}],
      "End": true
    },
    "Failed": {"Type": "Fail", "Error": "AddFailed"}
  }
}
EOF
aws stepfunctions create-state-machine --name adder --role-arn arn:aws:iam::000000000000:role/sfn-role \
  --definition file://task.json
aws stepfunctions start-execution --state-machine-arn arn:aws:states:us-east-1:000000000000:stateMachine:adder \
  --name sum --input '{"a":1,"b":2}'
understudy settle
# settled: no tracked work is pending
aws stepfunctions describe-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:adder:sum \
  --query '{status:status,output:output}'
# "status": "SUCCEEDED", "output": "{\"total\":3}"
aws stepfunctions get-execution-history --execution-arn arn:aws:states:us-east-1:000000000000:execution:adder:sum \
  --query 'events[].[id,previousEventId,type]' --output text
# 1  0  ExecutionStarted
# 2  0  TaskStateEntered
# 3  2  TaskScheduled
# 4  3  TaskStarted
# 5  4  TaskSucceeded
# 6  5  TaskStateExited
# 7  6  ExecutionSucceeded
```

The `lambda:invoke` result is the object AWS returns, `{ExecutedVersion, Payload, SdkHttpMetadata, SdkResponseMetadata, StatusCode}`, which `ResultSelector` reduces here. A function error is named by the function's `errorType`, with its error payload as the cause, and Retry and Catch match that name:

```sh
aws stepfunctions start-execution --state-machine-arn arn:aws:states:us-east-1:000000000000:stateMachine:adder \
  --name bad --input '{"a":1}'
understudy settle
aws stepfunctions get-execution-history --execution-arn arn:aws:states:us-east-1:000000000000:execution:adder:bad \
  --query 'events[?type==`TaskFailed`].taskFailedEventDetails'
# "resourceType": "lambda", "resource": "invoke", "error": "KeyError",
# "cause": "{\"errorMessage\": \"'b'\", \"errorType\": \"KeyError\", \"requestId\": \"76b2562b-...\", \"stackTrace\": [...]}"
aws stepfunctions describe-execution --execution-arn arn:aws:states:us-east-1:000000000000:execution:adder:bad \
  --query '{status:status,error:error}'
# "status": "FAILED", "error": "AddFailed"
```

A retry waits its interval, backoff and jitter on the emulator clock: advance the clock to release it. A Task's `TimeoutSeconds` is emulator time too. The function may be in another account: the serverless pattern `sfn-lambda-function-crossaccount-terraform` in `conformance/patterns` runs that end to end with Terraform.

Under `--iam-enforce enforce`, creating or updating a machine checks the caller's `iam:PassRole` on its role (with `iam:PassedToService` `states.amazonaws.com`), that the role trusts `states.amazonaws.com`, and, with logging on, the role's log-delivery permissions. A Task whose role may not `lambda:InvokeFunction` the function fails with `Lambda.AWSLambdaException`. After a run, `understudy iam policy --principal <role or user ARN>` prints the policy each needs ([policy.md](policy.md)).

## What is refused

Anything understudy does not run is refused when the machine is created, never accepted and ignored. A definition using JSONata, variables, Distributed Map, activities, callbacks or a Task resource other than Lambda answers `InvalidDefinition` with code `UNSUPPORTED_BY_UNDERSTUDY`, which `validate-state-machine-definition` reports as a diagnostic, so `terraform plan` shows it too:

```sh
cat > jsonata.json <<'EOF'
{"QueryLanguage": "JSONata", "StartAt": "P", "States": {"P": {"Type": "Pass", "End": true}}}
EOF
aws stepfunctions create-state-machine --name jsonata --role-arn arn:aws:iam::000000000000:role/sfn-role \
  --definition file://jsonata.json
# An error occurred (InvalidDefinition) when calling the CreateStateMachine operation: Invalid State Machine
# Definition: 'UNSUPPORTED_BY_UNDERSTUDY: understudy does not implement JSONata; QueryLanguage "JSONata" is
# refused. Run 'understudy coverage --service sfn' for what is supported. at /'
aws stepfunctions validate-state-machine-definition --definition file://jsonata.json
# "result": "FAIL",
# "diagnostics": [{"severity": "ERROR", "code": "UNSUPPORTED_BY_UNDERSTUDY", "message": "understudy does not implement JSONata; ...", "location": "/"}]
```

Express workflows, versions (`--publish`) and a customer managed KMS key answer 501:

```sh
aws stepfunctions create-state-machine --name express --type EXPRESS --role-arn arn:aws:iam::000000000000:role/sfn-role \
  --definition file://wait.json
# An error occurred (NotImplemented) when calling the CreateStateMachine operation: CreateStateMachine with type
# EXPRESS asks for an Express workflow, which understudy does not implement; nothing was changed. Leave type out or
# set it to STANDARD. Run 'understudy coverage --service sfn' for what is supported.
```

`start-sync-execution`, the activity, task-token, alias, version, redrive, `TestState` and Map Run operations answer 501 too.

Clients send `StartSyncExecution` to the host `sync-<endpoint host>`, so it reaches understudy, and its 501, only with host-prefix injection off: `AWS_DISABLE_HOST_PREFIX_INJECTION=true` for the CLI, `Config(inject_host_prefix=False)` for boto3, `disableHostPrefix: true` for JS v3. Otherwise the call fails before it is sent (`Could not connect to the endpoint URL: "http://sync-127.0.0.1:4566/"`).

## Terraform

`aws_sfn_state_machine` applies, plans empty and destroys with the provider block in [terraform.md](terraform.md), with tags and a `logging_configuration` to a log group (destination `<log group ARN>:*`). Logging and tracing settings are validated and stored; no log events or traces are written. `conformance/terraform/sfn.tf` is a working example with a Lambda task.

## Where it differs from AWS

Step Functions behavior follows a recording against AWS made on 2026-11-03, AWS's documentation and LocalStack's recordings of AWS. Where all three are silent, understudy chose, and the coverage notes name each choice with its recording case.

`understudy coverage --service sfn --switches` lists six fidelity switches, each defaulting to what AWS was recorded doing:

- the error of a Choice with no match, and of a missing Choice variable;
- the error of a failed intrinsic function;
- the name and cause of a Lambda service error;
- the status of an access denial;
- whether deleting a machine fails its running executions.

[Differences from AWS](differences.md#step-functions) lists what still differs.
