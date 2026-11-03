# SFN-06 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.7
- account ids masked as <acct> below

## Expected vs AWS

- 01 delete with running execution: 200 `{}`
- 02 describe after delete: status `DELETING` (machine still described)
- 03 start on deleting machine (StateMachineDeleting text, UNVERIFIED 5.4): 400 `StateMachineDeleting` `State Machine is being deleted: 'arn:aws:states:us-east-2:<acct>:stateMachine:us1103-del'`
- 04 create same name while deleting: 400 `StateMachineDeleting`, same text
- 05 list while deleting: the DELETING machine is still listed
- 06/07 running execution at its next transition (sfn.delete.running-execution, default ABORTED): differs: status `FAILED`, error `States.Runtime`, cause `State machine us1103-del has been deleted`; history ExecutionStarted, WaitStateEntered, ExecutionFailed (previousEventId 2) after the 30 s wait
- 08 describe after the execution closed (sleep 45): still `DELETING` (not yet gone)
- 09 idle machine deleted (sfn.delete.idle-machine, default removed at once): differs: Describe straight after delete answers 200 with `status: DELETING`, not `StateMachineDoesNotExist`
