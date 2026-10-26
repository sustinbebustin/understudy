"""Tests for scripts/parity-record. Run: uv run --with pytest pytest scripts/tests

Nothing here contacts AWS. Every subprocess run puts a stub `aws` first on
PATH, which logs its argv and answers `sts get-caller-identity` with a fixed
account, and gets an environment without AWS settings, whose config and
credentials files are /dev/null. The record-mode runs use a copy of the
script beside a stub distill-parity, under a temporary repository, so the
real testdata/parity is never rebuilt. A pseudo-terminal stands in for the
recording shell where the script requires one.
"""

import importlib.machinery
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import termios
import threading
from dataclasses import dataclass
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "parity-record"
_loader = importlib.machinery.SourceFileLoader("parity_record", str(SCRIPT))
_spec = importlib.util.spec_from_loader("parity_record", _loader)
pr = importlib.util.module_from_spec(_spec)
sys.modules["parity_record"] = pr  # dataclasses resolve annotations through sys.modules
_loader.exec_module(pr)

ACCOUNT = "111122223333"

# The contract's example: a key created outside rec, then the recorded step.
KMS = {
    "version": 1,
    "item": "KMS-alias",
    "account": "123456789012",
    "region": "us-east-2",
    "client": "aws-cli",
    "steps": [{"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"}],
    "record": [
        {
            "argv": ["aws", "kms", "create-key", "--description", "$P-alias", "--query", "KeyMetadata.KeyId",
                     "--output", "text"],
            "capture": "K",
        },
        {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias", "--alias-name", "alias/$P-dup",
                                           "--target-key-id", "$K"]},
    ],
}
KMS_PLAN = (
    "# kms/KMS-alias\n"
    'K=$(aws kms create-key --description "$P-alias" --query KeyMetadata.KeyId --output text)\n'
    'rec KMS-alias/01-duplicate aws kms create-alias --alias-name "alias/$P-dup" --target-key-id "$K"\n'
)

STUB_AWS = """#!{python}
import json, os, sys
args = sys.argv[1:]
with open(os.environ["STUB_AWS_LOG"], "a") as log:
    log.write(json.dumps(args) + "\\n")
if args[:2] == ["sts", "get-caller-identity"]:
    print(os.environ.get("STUB_AWS_CALLER", "{account}"))
    if os.environ.get("STUB_AWS_CALLER_EXIT"):
        print("Unable to locate credentials.", file=sys.stderr)
    sys.exit(int(os.environ.get("STUB_AWS_CALLER_EXIT", "0")))
if "--debug" in args:
    print("DEBUG - Sending http request: " + " ".join(args), file=sys.stderr)
print("stub " + " ".join(args[:2]))
sys.exit(254 if " ".join(args[:2]) == os.environ.get("STUB_AWS_FAIL") else 0)
"""

STUB_DISTILL = """#!{python}
import json, os, sys
from pathlib import Path
rec = Path(sys.argv[-1])
files = sorted(str(p.relative_to(rec)) for p in rec.rglob("*") if p.is_file())
with open(os.environ["STUB_DISTILL_LOG"], "a") as log:
    log.write(json.dumps({{"argv": sys.argv[1:], "files": files}}) + "\\n")
sys.exit(int(os.environ.get("STUB_DISTILL_EXIT", "0")))
"""


@dataclass
class Result:
    code: int
    out: str
    err: str


class Kit:
    """A temporary repository holding a copy of the script, its stubs and a recordings directory."""

    def __init__(self, root: Path):
        self.root = root
        self.script = root / "repo" / "scripts" / "parity-record"
        self.script.parent.mkdir(parents=True)
        shutil.copy(SCRIPT, self.script)
        self._stub(self.script.parent / "distill-parity", STUB_DISTILL)
        self.bin = root / "bin"
        self.bin.mkdir()
        self._stub(self.bin / "aws", STUB_AWS)
        self.rec = root / "rec"
        (self.rec / "_scripts").mkdir(parents=True)
        (self.rec / "_scripts" / "acct").write_text(ACCOUNT + "\n")
        self.aws_log = root / "aws.log"
        self.distill_log = root / "distill.log"

    def _stub(self, path: Path, text: str) -> None:
        path.write_text(text.format(python=sys.executable, account=ACCOUNT))
        path.chmod(0o755)

    def scenario(self, scenario_id: str, doc: dict | str) -> Path:
        path = self.root / "repo" / "testdata" / "parity" / scenario_id / "scenario.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(doc if isinstance(doc, str) else json.dumps(doc, indent=2) + "\n")
        return path

    def env(self, **overrides: str | None) -> dict[str, str]:
        env = {
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "HOME": str(self.root),
            "AWS_CONFIG_FILE": os.devnull,
            "AWS_SHARED_CREDENTIALS_FILE": os.devnull,
            "AWS_EC2_METADATA_DISABLED": "true",
            "STUB_AWS_LOG": str(self.aws_log),
            "STUB_DISTILL_LOG": str(self.distill_log),
            "UNDERSTUDY_RECORD_ACCOUNT": ACCOUNT,
            "REC": str(self.rec),
            "P": "usr-rec-20261026",
        }
        for k, v in overrides.items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v
        return env

    def aws_calls(self) -> list[list[str]]:
        return [json.loads(line) for line in self.aws_log.read_text().splitlines()] if self.aws_log.exists() else []

    def distill_calls(self) -> list[dict]:
        if not self.distill_log.exists():
            return []
        return [json.loads(line) for line in self.distill_log.read_text().splitlines()]

    def recordings(self) -> list[str]:
        return sorted(str(p.relative_to(self.rec)) for p in self.rec.rglob("*") if p.is_file())

    def run(self, *args: str, env: dict[str, str], typed: str | None = None,
            stdin_tty: bool = True, stdout_tty: bool = True) -> Result:
        """Run the script copy; typed goes to its stdin (EOF when None)."""
        cmd = [sys.executable, str(self.script), *args]
        if not stdin_tty and not stdout_tty:
            r = subprocess.run(cmd, env=env, input=typed or "", capture_output=True, text=True, timeout=60)
            return Result(r.returncode, r.stdout, r.stderr)
        master, slave = os.openpty()
        attrs = termios.tcgetattr(slave)
        attrs[3] &= ~termios.ECHO
        termios.tcsetattr(slave, termios.TCSANOW, attrs)
        proc = subprocess.Popen(
            cmd, env=env, text=True, stderr=subprocess.PIPE,
            stdin=slave if stdin_tty else subprocess.PIPE,
            stdout=slave if stdout_tty else subprocess.PIPE,
        )
        os.close(slave)
        chunks: list[bytes] = []
        reader = threading.Thread(target=_drain, args=(master, chunks))
        reader.start()
        if stdin_tty:
            os.write(master, typed.encode() if typed is not None else b"\x04")
            out, err = proc.communicate(timeout=60)
        else:
            out, err = proc.communicate(input=typed or "", timeout=60)
        reader.join(timeout=10)
        os.close(master)
        tty_out = b"".join(chunks).decode().replace("\r\n", "\n")
        return Result(proc.returncode, out if out is not None else tty_out, err)


def _drain(fd: int, chunks: list[bytes]) -> None:
    while True:
        try:
            data = os.read(fd, 4096)
        except OSError:  # EIO once every process holding the terminal has exited
            return
        if not data:
            return
        chunks.append(data)


@pytest.fixture
def kit(tmp_path):
    return Kit(tmp_path)


# --- plan mode -----------------------------------------------------------


@pytest.mark.parametrize(
    "arg,want",
    [
        ("KeyMetadata.KeyId", "KeyMetadata.KeyId"),
        ("alias/x@y%z+=:,._-1", "alias/x@y%z+=:,._-1"),
        ("", "''"),
        ("$P-alias", '"$P-alias"'),
        ("${P}x", '"${P}x"'),
        ('{"QueueName":"$P-std"}', '"{\\"QueueName\\":\\"$P-std\\"}"'),
        ("a`b\\$K", '"a\\`b\\\\$K"'),
        ("it's", "'it'\\''s'"),
        ("two words", "'two words'"),
        ('{"Pattern":"{}"}', "'{\"Pattern\":\"{}\"}'"),
    ],
)
def test_arguments_are_quoted_for_the_recording_shell(arg, want):
    assert pr.quote(arg) == want


@pytest.mark.parametrize(
    "arg",
    ["$P-alias", '{"QueueName":"$P-std"}', "a`b\\$K\"", "it's", "two words", "", "${P}x"],
)
def test_a_quoted_argument_is_one_word_with_its_references_expanded(arg):
    r = subprocess.run(
        ["bash", "-c", f"printf '%s\\0' {pr.quote(arg)}"],
        env={"PATH": os.environ["PATH"], "P": "usr", "K": "key"},
        capture_output=True, text=True, check=True,
    )
    assert r.stdout == arg.replace("${P}", "usr").replace("$P", "usr").replace("$K", "key") + "\0"


def test_plan_prints_each_command_in_the_kits_rec_form(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("kms/KMS-alias", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert (r.code, r.out, r.err) == (0, KMS_PLAN, "")
    assert kit.aws_calls() == []


def test_plan_reads_a_scenario_path_and_separates_scenarios_by_one_blank_line(kit):
    kms = kit.scenario("kms/KMS-alias", KMS)
    kit.scenario("sqs/R01", {**KMS, "item": "R01", "record": [
        {"argv": ["aws", "sqs", "purge-queue", "--queue-url", "$Q/$P-std"]},
        {"step": "03-delete-stale", "argv": ["aws", "sqs", "delete-message", "--receipt-handle", "$H1"]},
    ]})
    r = kit.run(str(kms), "sqs/R01", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert r.code == 0, r.err
    assert r.out == KMS_PLAN + (
        "\n# sqs/R01\n"
        'aws sqs purge-queue --queue-url "$Q/$P-std"\n'
        'rec R01/03-delete-stale aws sqs delete-message --receipt-handle "$H1"\n'
    )


def test_a_scenario_without_record_commands_is_refused(kit):
    doc = {k: v for k, v in KMS.items() if k != "record"}
    path = kit.scenario("kms/KMS-alias", doc)
    r = kit.run("kms/KMS-alias", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert (r.code, r.out) == (2, "")
    assert r.err == f"parity-record: {path} has no record commands: every step is recorded already\n"


def test_an_id_resolves_under_the_repository_of_the_script():
    # kms/KMS-alias is fully recorded, so plan mode finds nothing to print.
    r = subprocess.run([sys.executable, str(SCRIPT), "kms/KMS-alias"], capture_output=True, text=True)
    assert r.returncode == 2
    assert "has no record commands: every step is recorded already" in r.stderr


def test_an_unreadable_scenario_names_the_file(kit):
    r = kit.run("kms/missing", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert r.code == 2
    assert str(kit.root / "repo" / "testdata" / "parity" / "kms" / "missing" / "scenario.json") in r.err
    assert r.out == ""


def test_an_argument_that_is_neither_an_id_nor_a_path_is_refused(kit):
    r = kit.run("KMS alias", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert r.code == 2
    assert "'KMS alias' is neither a scenario id" in r.err


def _with_record(*entries):
    return {**KMS, "record": list(entries)}


REC_ENTRY = {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias"]}


@pytest.mark.parametrize(
    "doc,field",
    [
        ("{", "is not valid JSON"),
        ("[]", "the document"),
        ({**KMS, "version": 2}, "version"),
        ({**KMS, "version": True}, "version"),
        ({**KMS, "item": "R01"}, "item"),
        ({**KMS, "record": {}}, "record"),
        (_with_record("aws kms list-keys"), "record[0]"),
        (_with_record({"argv": ["curl", "https://kms.local"]}), "record[0].argv"),
        (_with_record({"argv": []}), "record[0].argv"),
        (_with_record({"step": "01-duplicate"}), "record[0].argv"),
        (_with_record({"argv": ["aws", 1]}), "record[0].argv"),
        (_with_record(REC_ENTRY, {"argv": ["aws", "sts", "x"], "shell": True}), "record[1].shell"),
        (_with_record({**REC_ENTRY, "step": "../x"}), "record[0].step"),
        (_with_record({**REC_ENTRY, "step": ".."}), "record[0].step"),
        (_with_record({**REC_ENTRY, "capture": "K"}), "record[0].capture"),
        (_with_record({"argv": ["aws", "kms"], "capture": "k"}), "record[0].capture"),
        (_with_record({"argv": ["aws", "kms", "describe-key", "--key-id", "$(id -u)"]}), "record[0].argv[4]"),
        (_with_record({"argv": ["aws", "kms", "describe-key", "--key-id", "${K:-x}"]}), "record[0].argv[4]"),
        (_with_record({"argv": ["aws", "kms", "describe-key", "--key-id", "$1"]}), "record[0].argv[4]"),
    ],
)
def test_a_malformed_scenario_is_refused_naming_the_member(kit, doc, field):
    path = kit.scenario("kms/KMS-alias", doc)
    r = kit.run("kms/KMS-alias", env=kit.env(), stdin_tty=False, stdout_tty=False)
    assert (r.code, r.out) == (2, "")
    assert r.err.startswith(f"parity-record: {path}")
    assert field in r.err
    assert "go test ./internal/parity" in r.err


# --- record mode: refusals -------------------------------------------------


def _refused(kit, r, *wanted):
    assert r.code == 2, r.err
    assert r.err.startswith("parity-record: ")
    for w in wanted:
        assert w in r.err
    assert kit.distill_calls() == []
    assert kit.recordings() == ["_scripts/acct"]


@pytest.mark.parametrize("stdin_tty,stdout_tty", [(False, False), (True, False), (False, True)])
def test_record_refuses_without_a_terminal(kit, stdin_tty, stdout_tty):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=ACCOUNT + "\n",
                stdin_tty=stdin_tty, stdout_tty=stdout_tty)
    _refused(kit, r, "terminal", "docs/recording-kit.md 1")
    assert kit.aws_calls() == []


@pytest.mark.parametrize("name", ["AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS"])
def test_record_refuses_while_an_endpoint_is_set(kit, name):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(**{name: "http://127.0.0.1:4566"}), typed=ACCOUNT + "\n")
    _refused(kit, r, name, f"unset {name}")
    assert kit.aws_calls() == []


def test_an_empty_endpoint_counts_as_set(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(AWS_ENDPOINT_URL=""), typed=ACCOUNT + "\n")
    _refused(kit, r, "AWS_ENDPOINT_URL")


def test_the_endpoint_check_comes_before_the_account_check(kit):
    kit.scenario("kms/KMS-alias", KMS)
    env = kit.env(AWS_ENDPOINT_URL="http://127.0.0.1:4566", UNDERSTUDY_RECORD_ACCOUNT=None)
    r = kit.run("--record", "kms/KMS-alias", env=env, typed=ACCOUNT + "\n")
    _refused(kit, r, "AWS_ENDPOINT_URL")
    assert "UNDERSTUDY_RECORD_ACCOUNT" not in r.err


@pytest.mark.parametrize("value,shown", [(None, "is unset"), ("12345", "'12345'"), ("1111222233334", "'1111222233334'"),
                                         ("", "''")])
def test_record_refuses_without_a_12_digit_account(kit, value, shown):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(UNDERSTUDY_RECORD_ACCOUNT=value), typed=ACCOUNT + "\n")
    _refused(kit, r, "UNDERSTUDY_RECORD_ACCOUNT", shown)
    assert kit.aws_calls() == []


@pytest.mark.parametrize("value", [None, ""])
def test_record_refuses_without_rec(kit, value):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(REC=value), typed=ACCOUNT + "\n")
    _refused(kit, r, "REC is not set")
    assert kit.aws_calls() == []


def test_record_refuses_without_the_account_file_distill_parity_reads(kit):
    kit.scenario("kms/KMS-alias", KMS)
    (kit.rec / "_scripts" / "acct").unlink()
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=ACCOUNT + "\n")
    assert r.code == 2
    assert str(kit.rec / "_scripts" / "acct") in r.err
    assert f'echo {ACCOUNT} > "$REC/_scripts/acct"' in r.err
    assert kit.aws_calls() == [] and kit.recordings() == []


def test_record_refuses_a_recordings_directory_of_another_account(kit):
    kit.scenario("kms/KMS-alias", KMS)
    (kit.rec / "_scripts" / "acct").write_text("999988887777\n")
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=ACCOUNT + "\n")
    _refused(kit, r, "'999988887777'", ACCOUNT)
    assert kit.aws_calls() == []


def test_record_refuses_a_reference_neither_set_nor_captured_earlier(kit):
    kit.scenario("kms/KMS-alias", _with_record(
        {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias", "--target-key-id", "$K"]},
        {"argv": ["aws", "kms", "create-key"], "capture": "K"},
        {"argv": ["aws", "kms", "describe-key", "--key-id", "${K}", "--region", "$R"]},
    ))
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=ACCOUNT + "\n")
    _refused(kit, r, "$K (kms/KMS-alias record[0])", "$R (kms/KMS-alias record[2])")
    assert "$K (kms/KMS-alias record[2])" not in r.err  # record[1] captured it
    assert kit.aws_calls() == []


def test_a_capture_does_not_carry_into_the_next_scenario(kit):
    kit.scenario("kms/KMS-alias", KMS)
    kit.scenario("kms/KMS-other", {**KMS, "item": "KMS-other", "record": [
        {"step": "01-duplicate", "argv": ["aws", "kms", "describe-key", "--key-id", "$K"]},
    ]})
    r = kit.run("--record", "kms/KMS-alias", "kms/KMS-other", env=kit.env(), typed=ACCOUNT + "\n")
    _refused(kit, r, "$K (kms/KMS-other record[0])")


def test_record_refuses_credentials_of_another_account_showing_both(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_AWS_CALLER="999988887777"), typed=ACCOUNT + "\n")
    _refused(kit, r, "999988887777", ACCOUNT)
    assert kit.aws_calls() == [["sts", "get-caller-identity", "--query", "Account", "--output", "text"]]


def test_record_refuses_when_the_caller_identity_fails(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_AWS_CALLER_EXIT="255"), typed=ACCOUNT + "\n")
    _refused(kit, r, "exited 255", "Unable to locate credentials.", "aws login")
    assert len(kit.aws_calls()) == 1


def test_record_refuses_without_aws_on_path(kit):
    kit.scenario("kms/KMS-alias", KMS)
    (kit.bin / "aws").unlink()
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(PATH=str(kit.bin)), typed=ACCOUNT + "\n")
    _refused(kit, r, "cannot run aws")


@pytest.mark.parametrize("typed", [None, "\n", "y\n", "yes\n", "11112222333\n", "999988887777\n"])
def test_record_runs_nothing_unless_the_account_id_is_typed(kit, typed):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=typed)
    prompt = f"Record 2 commands in account {ACCOUNT}? Type the account id to continue: "
    assert r.err.startswith(prompt), r.err
    # On EOF nothing echoes a newline, so the script ends the prompt line itself.
    refusal = r.err.removeprefix(prompt).removeprefix("\n" if typed is None else "")
    _refused(kit, Result(r.code, r.out, refusal), "nothing was run", f"type {ACCOUNT}")
    assert r.out == KMS_PLAN
    assert kit.aws_calls() == [["sts", "get-caller-identity", "--query", "Account", "--output", "text"]]


# --- record mode: the run ------------------------------------------------------


def test_record_runs_the_commands_in_order_then_distills(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(), typed=f" {ACCOUNT} \n")
    assert r.code == 0, r.err
    assert r.out == KMS_PLAN
    assert kit.aws_calls() == [
        ["sts", "get-caller-identity", "--query", "Account", "--output", "text"],
        ["kms", "create-key", "--description", "usr-rec-20261026-alias", "--query", "KeyMetadata.KeyId",
         "--output", "text"],
        ["kms", "create-alias", "--alias-name", "alias/usr-rec-20261026-dup", "--target-key-id", "stub kms create-key",
         "--debug"],
    ]
    out = (kit.rec / "KMS-alias" / "01-duplicate.out").read_text()
    assert out == "stub kms create-alias\nexit=0\n"
    http = (kit.rec / "KMS-alias" / "01-duplicate.http").read_text()
    assert http.startswith("DEBUG - Sending http request: kms create-alias")
    assert kit.distill_calls() == [{
        "argv": ["--replace", str(kit.rec)],
        "files": ["KMS-alias/01-duplicate.http", "KMS-alias/01-duplicate.out", "_scripts/acct"],
    }]


def test_the_exit_status_is_distill_paritys(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_DISTILL_EXIT="3"), typed=ACCOUNT + "\n")
    assert r.code == 3, r.err
    assert len(kit.distill_calls()) == 1


def test_a_failing_rec_command_is_recorded_and_the_run_goes_on(kit):
    kit.scenario("kms/KMS-alias", _with_record(
        {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias"]},
        {"step": "02-again", "argv": ["aws", "kms", "describe-key"]},
    ))
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_AWS_FAIL="kms create-alias"), typed=ACCOUNT + "\n")
    assert r.code == 0, r.err
    assert (kit.rec / "KMS-alias" / "01-duplicate.out").read_text() == "stub kms create-alias\nexit=254\n"
    assert (kit.rec / "KMS-alias" / "02-again.out").read_text() == "stub kms describe-key\nexit=0\n"
    assert len(kit.distill_calls()) == 1


def test_a_command_without_capture_inherits_its_output_and_its_exit_status_is_ignored(kit):
    kit.scenario("kms/KMS-alias", _with_record(
        {"argv": ["aws", "kms", "create-key"]},
        {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias"]},
    ))
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_AWS_FAIL="kms create-key"), typed=ACCOUNT + "\n")
    assert r.code == 0, r.err
    assert "stub kms create-key\n" in r.out
    assert [c[:2] for c in kit.aws_calls()] == [["sts", "get-caller-identity"], ["kms", "create-key"],
                                                ["kms", "create-alias"]]


def test_a_failing_capture_stops_the_run_before_distill_parity(kit):
    kit.scenario("kms/KMS-alias", KMS)
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(STUB_AWS_FAIL="kms create-key"), typed=ACCOUNT + "\n")
    assert r.code == 1
    assert "$K" in r.err and "exited 254" in r.err and str(kit.rec) in r.err
    assert [c[:2] for c in kit.aws_calls()] == [["sts", "get-caller-identity"], ["kms", "create-key"]]
    assert kit.distill_calls() == []
    assert kit.recordings() == ["_scripts/acct"]


def test_a_capture_overrides_the_environment_and_the_environment_fills_the_rest(kit):
    kit.scenario("kms/KMS-alias", _with_record(
        {"argv": ["aws", "kms", "create-key"], "capture": "P"},
        {"step": "01-duplicate", "argv": ["aws", "kms", "tag-resource", "--key-id", "${P}/$R"]},
    ))
    r = kit.run("--record", "kms/KMS-alias", env=kit.env(R="us-east-2"), typed=ACCOUNT + "\n")
    assert r.code == 0, r.err
    assert kit.aws_calls()[-1] == ["kms", "tag-resource", "--key-id", "stub kms create-key/us-east-2", "--debug"]


def test_several_scenarios_run_in_order_under_one_confirmation(kit):
    kit.scenario("kms/KMS-alias", KMS)
    kit.scenario("sqs/R01", {**KMS, "item": "R01", "record": [
        {"step": "03-delete-stale", "argv": ["aws", "sqs", "delete-message"]},
    ]})
    r = kit.run("--record", "kms/KMS-alias", "sqs/R01", env=kit.env(), typed=ACCOUNT + "\n")
    assert r.code == 0, r.err
    assert f"Record 3 commands in account {ACCOUNT}?" in r.err
    assert [c[:2] for c in kit.aws_calls()] == [["sts", "get-caller-identity"], ["kms", "create-key"],
                                                ["kms", "create-alias"], ["sqs", "delete-message"]]
    assert (kit.rec / "R01" / "03-delete-stale.out").exists()
    assert len(kit.distill_calls()) == 1
