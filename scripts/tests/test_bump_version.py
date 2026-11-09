"""Tests for scripts/bump-version. Run: uv run --with pytest pytest scripts/tests"""

import importlib.machinery
import importlib.util
import sys
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "bump-version"
_loader = importlib.machinery.SourceFileLoader("bump_version", str(SCRIPT))
_spec = importlib.util.spec_from_loader("bump_version", _loader)
bv = importlib.util.module_from_spec(_spec)
sys.modules["bump_version"] = bv
_loader.exec_module(bv)

REPO = Path(__file__).resolve().parent.parent.parent

CHANGELOG = """# Changelog

Intro.

## [Unreleased]

### Fixed

- A fix.

## [0.1.1] - 2026-11-09

### Fixed

- Older.
"""


def tree(tmp_path: Path, version: str = "0.1.1") -> Path:
    """The files bump-version edits, copied from the repository at `version`."""
    for pin in bv.PINS:
        text = (REPO / pin.path).read_text()
        current = pin.read(text)
        dest = tmp_path / pin.path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(text.replace(current, version) if current else text)
    (tmp_path / "CHANGELOG.md").write_text(CHANGELOG)
    return tmp_path


def test_every_pin_matches_in_the_repository():
    versions = bv.read_versions(REPO)
    assert set(versions) == {pin.path for pin in bv.PINS}
    assert len(set(versions.values())) == 1, versions


def test_check_names_the_file_that_disagrees(tmp_path):
    root = tree(tmp_path)
    package = root / "testcontainers/node/package.json"
    package.write_text(package.read_text().replace('"version": "0.1.1"', '"version": "0.1.2"'))
    problems = bv.check(root)
    assert len(problems) == 1
    assert "testcontainers/node/package.json" in problems[0]
    assert "0.1.2" in problems[0]


def test_bump_rewrites_every_pin_and_cuts_the_changelog(tmp_path):
    root = tree(tmp_path)
    ran = []
    bv.bump(root, "0.2.0", "2026-12-01", run=lambda cmd, cwd: ran.append((cmd, cwd.relative_to(root).as_posix())))
    assert set(bv.read_versions(root).values()) == {"0.2.0"}
    assert (["uv", "lock", "--quiet"], "testcontainers/python") in ran
    changelog = (root / "CHANGELOG.md").read_text()
    assert "## [Unreleased]\n\n## [0.2.0] - 2026-12-01\n\n### Fixed\n\n- A fix.\n\n## [0.1.1]" in changelog


def test_bump_refuses_an_empty_unreleased_section(tmp_path):
    root = tree(tmp_path)
    (root / "CHANGELOG.md").write_text(CHANGELOG.replace("### Fixed\n\n- A fix.\n\n", "", 1))
    with pytest.raises(bv.BumpError, match=r"\[Unreleased\] is empty"):
        bv.bump(root, "0.2.0", "2026-12-01", run=lambda cmd, cwd: None)
    assert set(bv.read_versions(root).values()) == {"0.1.1"}


@pytest.mark.parametrize("version", ["v0.2.0", "0.2", "0.2.0-rc.1", "0.1.1", "0.1.0"])
def test_bump_refuses_a_version_that_is_not_a_newer_release(tmp_path, version):
    root = tree(tmp_path)
    with pytest.raises(bv.BumpError):
        bv.bump(root, version, "2026-12-01", run=lambda cmd, cwd: None)
    assert set(bv.read_versions(root).values()) == {"0.1.1"}
