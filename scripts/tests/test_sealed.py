"""Tests for scripts/sealed. Run: uv run --with pytest pytest scripts/tests"""

import importlib.machinery
import importlib.util
import os
import subprocess
import sys
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "sealed"
_loader = importlib.machinery.SourceFileLoader("sealed", str(SCRIPT))
_spec = importlib.util.spec_from_loader("sealed", _loader)
sealed = importlib.util.module_from_spec(_spec)
_loader.exec_module(sealed)


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1:4566",
        "http://127.8.9.10:1",
        "http://localhost:4566",
        "https://LOCALHOST",
        "http://s3.localhost:4566",
        "http://bucket.s3.localhost:4566/key",
        "http://[::1]:4566",
    ],
)
def test_loopback_urls_are_accepted(url):
    assert sealed.is_loopback_url(url)


@pytest.mark.parametrize(
    "url",
    [
        "https://sqs.us-east-1.amazonaws.com",
        "http://localhost.evil.com",
        "http://10.0.0.5:4566",
        "http://0.0.0.0:4566",
        "http://user:pw@127.0.0.1:4566",
        "file://127.0.0.1/x",
        "127.0.0.1:4566",
        "http://127.0.0.1:notaport",
        "",
    ],
)
def test_other_urls_are_refused(url):
    assert not sealed.is_loopback_url(url)


def test_inherited_aws_settings_are_dropped_and_replaced():
    inherited = {
        "PATH": "/usr/bin",
        "AWS_PROFILE": "prod",
        "AWS_SESSION_TOKEN": "tok",
        "AWS_ACCESS_KEY_ID": "AKIAREAL",
        "AWS_ENDPOINT_URL_S3": "https://s3.amazonaws.com",
        "AWS_ROLE_ARN": "arn:aws:iam::1:role/x",
        "AWS_MAX_ATTEMPTS": "3",
    }
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-west-1", "000000000000")
    assert env["PATH"] == "/usr/bin"
    assert env["AWS_MAX_ATTEMPTS"] == "3"
    for gone in ("AWS_PROFILE", "AWS_SESSION_TOKEN", "AWS_ENDPOINT_URL_S3", "AWS_ROLE_ARN"):
        assert gone not in env
    assert env["AWS_ACCESS_KEY_ID"] == "000000000000"
    assert env["AWS_ENDPOINT_URL"] == "http://127.0.0.1:4566"
    assert env["AWS_REGION"] == env["AWS_DEFAULT_REGION"] == "us-west-1"
    assert env["AWS_CONFIG_FILE"] == env["AWS_SHARED_CREDENTIALS_FILE"] == os.devnull
    assert env["AWS_EC2_METADATA_DISABLED"] == "true"


def test_remote_endpoint_is_refused():
    with pytest.raises(sealed.SealError, match="not a loopback URL"):
        sealed.sealed_env({}, "https://sqs.us-east-1.amazonaws.com", "us-east-1", "test")


def test_print_env_unsets_dropped_variables():
    inherited = {"AWS_PROFILE": "prod", "HOME": "/h"}
    env = sealed.sealed_env(inherited, "http://localhost:4566", "us-east-1", "test")
    out = sealed.shell_exports(inherited, env)
    assert "unset AWS_PROFILE" in out
    assert "export AWS_ENDPOINT_URL=http://localhost:4566" in out
    assert "HOME" not in out


def run(*args, env=None):
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        capture_output=True,
        text=True,
        env={**os.environ, **(env or {})},
    )


def test_command_runs_with_sealed_environment_and_its_exit_status():
    r = run("sh", "-c", 'echo "$AWS_ENDPOINT_URL $AWS_PROFILE"; exit 7', env={"AWS_PROFILE": "prod"})
    assert r.returncode == 7
    assert r.stdout.strip() == "http://127.0.0.1:4566"


def test_double_dash_separates_the_command():
    r = run("--endpoint", "http://localhost:1", "--", "sh", "-c", 'echo "$AWS_ENDPOINT_URL"')
    assert r.returncode == 0
    assert r.stdout.strip() == "http://localhost:1"


def test_remote_endpoint_exits_2_without_running_the_command():
    r = run("--endpoint", "https://s3.amazonaws.com", "sh", "-c", "echo ran")
    assert r.returncode == 2
    assert "ran" not in r.stdout
    assert "not a loopback URL" in r.stderr


def test_missing_and_unknown_commands_exit_2():
    assert run().returncode == 2
    r = run("definitely-not-a-command-xyz")
    assert r.returncode == 2
    assert "command not found" in r.stderr
