"""Tests for what scripts/distill-parity keeps when it rebuilds its output.

Run: uv run --with pytest pytest scripts/tests

The replay scenarios (testdata/parity/<svc>/<item>/scenario.json) are written
by hand beside the distilled fixtures, so a rebuild must not delete them. The
account 111122223333 stands in for a recorded owner account.
"""

import importlib.machinery
import importlib.util
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


def test_replace_keeps_the_scenario_files_and_drops_stale_fixtures(tmp_path):
    rec = recordings(tmp_path)
    out = tmp_path / "parity"
    (out / "kms" / "KMS-alias").mkdir(parents=True)
    (out / "kms" / "KMS-alias" / "scenario.json").write_text(SCENARIO)
    (out / "kms" / "KMS-alias" / "09-stale.json").write_text("{}\n")
    (out / "sqs" / "R01").mkdir(parents=True)
    (out / "sqs" / "R01" / "scenario.json").write_text(SCENARIO.replace("KMS-alias", "R01"))

    assert dp.main(["--replace", "--out", str(out), str(rec)]) == 0

    assert (out / "kms" / "KMS-alias" / "scenario.json").read_text() == SCENARIO
    assert (out / "sqs" / "R01" / "scenario.json").read_text() == SCENARIO.replace("KMS-alias", "R01")
    assert not (out / "kms" / "KMS-alias" / "09-stale.json").exists()
    assert (out / "kms" / "KMS-alias" / "notes.md").read_text() == f"recorded in {dp.FAKE_ACCOUNT}\n"


def test_a_missing_account_file_is_refused_without_touching_the_output(tmp_path, capsys):
    rec = recordings(tmp_path)
    (rec / "_scripts" / "acct").unlink()
    out = tmp_path / "parity"
    (out / "kms" / "KMS-alias").mkdir(parents=True)
    (out / "kms" / "KMS-alias" / "01-duplicate.json").write_text("{}\n")

    assert dp.main(["--replace", "--out", str(out), str(rec)]) == 2

    err = capsys.readouterr().err
    assert str(rec / "_scripts" / "acct") in err
    assert "--account-file" in err
    assert (out / "kms" / "KMS-alias" / "01-duplicate.json").exists()
