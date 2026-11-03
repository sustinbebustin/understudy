"""Tests for what scripts/distill-parity keeps when it writes into its output.

Run: uv run --with pytest pytest scripts/tests

A recordings directory may hold only some steps of some items, so distilling
it replaces the step fixtures it holds and leaves every other fixture alone. The
replay scenarios (testdata/parity/<svc>/<item>/scenario.json) are written by
hand beside the distilled fixtures, so distilling never deletes them. The
account 111122223333 stands in for a recorded owner account.
"""

import importlib.machinery
import importlib.util
import json
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / "distill-parity"
_loader = importlib.machinery.SourceFileLoader("distill_parity", str(SCRIPT))
_spec = importlib.util.spec_from_loader("distill_parity", _loader)
dp = importlib.util.module_from_spec(_spec)
sys.modules["distill_parity"] = dp  # dataclasses resolve annotations through sys.modules
_loader.exec_module(dp)

OWNER = "111122223333"
SCENARIO = '{\n  "version": 1,\n  "item": "KMS-alias",\n  "account": "123456789012"\n}\n'


def recordings(tmp_path: Path) -> Path:
    rec = tmp_path / "rec"
    (rec / "_scripts").mkdir(parents=True)
    (rec / "_scripts" / "acct").write_text(OWNER + "\n")
    (rec / "KMS-alias").mkdir()
    (rec / "KMS-alias" / "notes.md").write_text(f"recorded in {OWNER}\n")
    return rec


def kinesis_capture(rec: Path, body: str) -> None:
    """A recurl capture of KIN-01: response, -v trace and the request body recurl saves as .req."""
    item = rec / "KIN-01"
    item.mkdir()
    (item / "01-list.http").write_bytes(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
    (item / "01-list.trace").write_text(
        "> POST / HTTP/1.1\r\n> Host: kinesis.us-east-2.amazonaws.com\r\n"
        f"> Content-Length: {len(body)}\r\n> \r\n* upload completely sent off: {len(body)} bytes\r\n"
    )
    (item / "01-list.req").write_text(body)


def test_distilling_replaces_only_the_steps_recorded_and_keeps_scenarios(tmp_path):
    rec = recordings(tmp_path)
    out = tmp_path / "parity"
    (out / "kms" / "KMS-alias").mkdir(parents=True)
    (out / "kms" / "KMS-alias" / "scenario.json").write_text(SCENARIO)
    (out / "kms" / "KMS-alias" / "09-earlier.json").write_text("{}\n")
    (out / "kms" / "KMS-alias" / "notes.md").write_text("old notes\n")
    (out / "sqs" / "R01").mkdir(parents=True)
    (out / "sqs" / "R01" / "scenario.json").write_text(SCENARIO.replace("KMS-alias", "R01"))
    (out / "sqs" / "R01" / "03-delete-stale.json").write_text("{}\n")
    (out / "sqs" / "R01" / "notes.md").write_text("R01 notes\n")

    assert dp.main(["--out", str(out), str(rec)]) == 0

    assert (out / "kms" / "KMS-alias" / "scenario.json").read_text() == SCENARIO
    assert (out / "kms" / "KMS-alias" / "09-earlier.json").read_text() == "{}\n"
    assert (out / "kms" / "KMS-alias" / "notes.md").read_text() == f"recorded in {dp.FAKE_ACCOUNT}\n"
    assert (out / "sqs" / "R01" / "scenario.json").read_text() == SCENARIO.replace("KMS-alias", "R01")
    assert (out / "sqs" / "R01" / "03-delete-stale.json").read_text() == "{}\n"
    assert (out / "sqs" / "R01" / "notes.md").read_text() == "R01 notes\n"


def test_a_new_step_of_a_recorded_item_keeps_its_other_steps(tmp_path):
    rec = recordings(tmp_path)
    kinesis_capture(rec, "{}")
    out = tmp_path / "parity"
    (out / "kinesis" / "KIN-01").mkdir(parents=True)
    (out / "kinesis" / "KIN-01" / "scenario.json").write_text(SCENARIO.replace("KMS-alias", "KIN-01"))
    (out / "kinesis" / "KIN-01" / "00-create.json").write_text("{}\n")
    (out / "kinesis" / "KIN-01" / "00-shards.tsv").write_text("a\tb\n")

    assert dp.main(["--out", str(out), str(rec)]) == 0

    assert (out / "kinesis" / "KIN-01" / "00-create.json").read_text() == "{}\n"
    assert (out / "kinesis" / "KIN-01" / "00-shards.tsv").read_text() == "a\tb\n"
    assert (out / "kinesis" / "KIN-01" / "01-list.json").exists()


def test_a_recorded_step_replaces_its_fixture_under_any_service(tmp_path):
    rec = recordings(tmp_path)
    kinesis_capture(rec, "{}")
    out = tmp_path / "parity"
    (out / "sqs" / "KIN-01").mkdir(parents=True)
    (out / "sqs" / "KIN-01" / "01-list.json").write_text("{}\n")

    assert dp.main(["--out", str(out), str(rec)]) == 0

    assert not (out / "sqs" / "KIN-01").exists()
    assert (out / "kinesis" / "KIN-01" / "01-list.json").exists()


def test_a_step_that_fails_to_distill_keeps_its_committed_fixture(tmp_path, capsys):
    rec = recordings(tmp_path)
    kinesis_capture(rec, "{}")
    (rec / "KIN-01" / "01-list.req").write_text("{ }")  # one byte longer than the trace says
    out = tmp_path / "parity"
    (out / "kinesis" / "KIN-01").mkdir(parents=True)
    (out / "kinesis" / "KIN-01" / "01-list.json").write_text('{"committed": true}\n')

    assert dp.main(["--out", str(out), str(rec)]) == 1

    assert "KIN-01/01-list" in capsys.readouterr().err
    assert (out / "kinesis" / "KIN-01" / "01-list.json").read_text() == '{"committed": true}\n'


def test_a_file_that_is_no_capture_or_side_observation_is_refused_by_name(tmp_path, capsys):
    rec = recordings(tmp_path)
    kinesis_capture(rec, "{}")
    (rec / "KIN-01" / "eleven-mb.json").write_text("{}")
    (rec / "KIN-01" / "fn.zip").write_bytes(b"PK")
    (rec / "KIN-01" / "02-shards.tsv").write_text("a\tb\n")
    out = tmp_path / "parity"

    assert dp.main(["--out", str(out), str(rec)]) == 2

    err = capsys.readouterr().err
    assert "KIN-01/eleven-mb.json" in err and "KIN-01/fn.zip" in err and "02-shards.tsv" not in err
    assert not out.exists()


def test_replace_is_refused_without_touching_the_output(tmp_path, capsys):
    rec = recordings(tmp_path)
    out = tmp_path / "parity"
    (out / "sqs" / "R01").mkdir(parents=True)
    (out / "sqs" / "R01" / "03-delete-stale.json").write_text("{}\n")

    assert dp.main(["--replace", "--out", str(out), str(rec)]) == 2

    assert "--replace" in capsys.readouterr().err
    assert (out / "sqs" / "R01" / "03-delete-stale.json").exists()


def test_an_unknown_item_directory_is_refused_by_name_without_touching_the_output(tmp_path, capsys):
    rec = recordings(tmp_path)
    (rec / "TC-01").mkdir()
    (rec / "ESMS-src").mkdir()
    (rec / "_tmp").mkdir()
    out = tmp_path / "parity"
    (out / "kms" / "KMS-alias").mkdir(parents=True)
    (out / "kms" / "KMS-alias" / "09-stale.json").write_text("{}\n")

    assert dp.main(["--out", str(out), str(rec)]) == 2

    err = capsys.readouterr().err
    assert "TC-01" in err and "ESMS-src" in err and "_tmp" not in err
    assert "--skip" in err
    assert (out / "kms" / "KMS-alias" / "09-stale.json").exists()


def test_skip_leaves_named_directories_out(tmp_path):
    rec = recordings(tmp_path)
    (rec / "TC-01").mkdir()
    (rec / "TC-01" / "engine.txt").write_text("27.0 linux\n")
    out = tmp_path / "parity"

    assert dp.main(["--skip", "TC-01", "--out", str(out), str(rec)]) == 0

    assert not list(out.rglob("TC-01"))
    assert (out / "kms" / "KMS-alias" / "notes.md").exists()


def test_a_recording_with_no_items_is_refused(tmp_path, capsys):
    rec = recordings(tmp_path)
    (rec / "KMS-alias" / "notes.md").unlink()
    (rec / "KMS-alias").rmdir()

    assert dp.main(["--out", str(tmp_path / "parity"), str(rec)]) == 2

    assert "no item directories" in capsys.readouterr().err


def test_queue_item_curl_capture_keeps_its_masked_request_body(tmp_path):
    rec = recordings(tmp_path)
    body = json.dumps({"StreamARN": f"arn:aws:kinesis:us-east-2:{OWNER}:stream/s"})
    kinesis_capture(rec, body)
    out = tmp_path / "parity"

    assert dp.main(["--out", str(out), str(rec)]) == 0

    fixture = json.loads((out / "kinesis" / "KIN-01" / "01-list.json").read_text())
    assert fixture["exchanges"][0]["request"]["body"] == body.replace(OWNER, dp.FAKE_ACCOUNT)
    assert not (out / "kinesis" / "KIN-01" / "01-list.req").exists()


def test_the_credential_check_covers_request_bodies(tmp_path, capsys):
    rec = recordings(tmp_path)
    kinesis_capture(rec, '{"refreshToken": "x"}')
    out = tmp_path / "parity"

    assert dp.main(["--out", str(out), str(rec)]) == 1

    assert str(out / "kinesis" / "KIN-01" / "01-list.json") in capsys.readouterr().err


def test_a_missing_account_file_is_refused_without_touching_the_output(tmp_path, capsys):
    rec = recordings(tmp_path)
    (rec / "_scripts" / "acct").unlink()
    out = tmp_path / "parity"
    (out / "kms" / "KMS-alias").mkdir(parents=True)
    (out / "kms" / "KMS-alias" / "01-duplicate.json").write_text("{}\n")

    assert dp.main(["--out", str(out), str(rec)]) == 2

    err = capsys.readouterr().err
    assert str(rec / "_scripts" / "acct") in err
    assert "--account-file" in err
    assert (out / "kms" / "KMS-alias" / "01-duplicate.json").exists()
