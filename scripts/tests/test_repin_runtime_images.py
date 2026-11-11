"""Tests for scripts/repin-runtime-images. Run: uv run --with pytest pytest scripts/tests

The registry is a stub: no test reaches the network.
"""

import hashlib
import importlib.machinery
import importlib.util
import json
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "repin-runtime-images"
_loader = importlib.machinery.SourceFileLoader("repin_runtime_images", str(SCRIPT))
_spec = importlib.util.spec_from_loader("repin_runtime_images", _loader)
rp = importlib.util.module_from_spec(_spec)
sys.modules["repin_runtime_images"] = rp
_loader.exec_module(rp)

REPO = Path(__file__).resolve().parent.parent.parent
TABLE = "internal/service/lambda/launcher/images.go"

INDEX = "application/vnd.oci.image.index.v1+json"
MANIFEST = "application/vnd.oci.image.manifest.v1+json"
TOKEN = "anonymous-token"

# Every tag of the table as it stood when this test was written; a new row
# needs a stub answer here.
TAGS = {
    ("lambda/provided", "al2023"),
    ("lambda/python", "3.13"),
    ("lambda/python", "3.12"),
    ("lambda/nodejs", "22"),
    ("lambda/nodejs", "20"),
    ("lambda/java", "21"),
    ("lambda/dotnet", "8"),
    ("lambda/ruby", "3.3"),
}


class Registry:
    """public.ecr.aws's anonymous token and manifest endpoints, from a table of answers."""

    def __init__(self, answers: dict[tuple[str, str], tuple[str, bytes]]):
        self.answers = answers

    def __call__(self, req: urllib.request.Request) -> "rp.Response":
        url = req.full_url
        token = re.fullmatch(r"https://public\.ecr\.aws/token/\?scope=repository:(.+):pull", url)
        if token:
            return rp.Response(content_type="application/json", digest=None, body=json.dumps({"token": TOKEN}).encode())
        m = re.fullmatch(r"https://public\.ecr\.aws/v2/(.+)/manifests/(.+)", url)
        assert m, f"unexpected request {url}"
        if req.get_header("Authorization") != f"Bearer {TOKEN}":
            raise urllib.error.HTTPError(url, 401, "Unauthorized", {}, None)
        assert INDEX in (req.get_header("Accept") or "")
        answer = self.answers.get((m.group(1), m.group(2)))
        if answer is None:
            raise urllib.error.HTTPError(url, 404, "Not Found", {}, None)
        media_type, body = answer
        return rp.Response(content_type=media_type, digest=digest_of(body), body=body)


def digest_of(body: bytes) -> str:
    return "sha256:" + hashlib.sha256(body).hexdigest()


def new_index(repository: str, tag: str) -> bytes:
    return json.dumps({"mediaType": INDEX, "manifests": [], "annotations": {"for": f"{repository}:{tag}"}}).encode()


def every_tag_as(media_type: str = INDEX) -> dict[tuple[str, str], tuple[str, bytes]]:
    return {(repository, tag): (media_type, new_index(repository, tag)) for repository, tag in TAGS}


def tree(tmp_path: Path) -> Path:
    dest = tmp_path / TABLE
    dest.parent.mkdir(parents=True)
    dest.write_text((REPO / TABLE).read_text())
    return tmp_path


def test_the_repository_table_has_the_tags_the_stub_answers():
    pins = rp.read_pins((REPO / TABLE).read_text())
    assert {(pin.repository, pin.tag) for pin in pins} == TAGS


def test_repin_rewrites_every_digest_and_the_date(tmp_path):
    root = tree(tmp_path)
    answers = every_tag_as(INDEX)
    rp.repin(root, "2026-12-01", Registry(answers))
    text = (root / TABLE).read_text()
    assert "public.ecr.aws served it on 2026-12-01." in text
    assert "2026-10-14" not in text
    for (repository, tag), (_, body) in answers.items():
        row = re.search(rf'Repository: repo \+ "{repository.removeprefix("lambda/")}", Tag: "{re.escape(tag)}", Digest: "([^"]+)"', text)
        assert row, f"no row for {repository}:{tag}"
        assert row.group(1) == digest_of(body)


def test_repin_names_an_unresolvable_tag_and_changes_nothing(tmp_path):
    root = tree(tmp_path)
    before = (root / TABLE).read_text()
    answers = every_tag_as(INDEX)
    del answers[("lambda/java", "21")]
    with pytest.raises(rp.RepinError, match=r"public\.ecr\.aws/lambda/java:21") as err:
        rp.repin(root, "2026-12-01", Registry(answers))
    assert "404" in str(err.value)
    assert (root / TABLE).read_text() == before


def test_repin_refuses_a_single_architecture_manifest_and_changes_nothing(tmp_path):
    root = tree(tmp_path)
    before = (root / TABLE).read_text()
    answers = every_tag_as(INDEX)
    answers[("lambda/ruby", "3.3")] = (MANIFEST, new_index("lambda/ruby", "3.3"))
    with pytest.raises(rp.RepinError, match=r"public\.ecr\.aws/lambda/ruby:3\.3") as err:
        rp.repin(root, "2026-12-01", Registry(answers))
    assert MANIFEST in str(err.value)
    assert (root / TABLE).read_text() == before
