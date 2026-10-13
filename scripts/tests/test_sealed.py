"""Tests for scripts/sealed. Run: uv run --with pytest pytest scripts/tests

Side effect: every test that runs the script as a subprocess (run()) writes
sealed's real <repo>/.cache (sealed.tfrc, .gitignore, terraform-plugins),
because sealed always keeps its Terraform config there. That directory ignores
itself in git, but it must be writable for the suite to pass.
"""

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


def test_inherited_terraform_credentials_are_dropped():
    inherited = {
        "TF_TOKEN_app_terraform_io": "tok",
        "TF_CLOUD_ORGANIZATION": "acme",
        "TF_CLOUD_HOSTNAME": "app.terraform.io",
        "TF_LOG": "DEBUG",
    }
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    for gone in ("TF_TOKEN_app_terraform_io", "TF_CLOUD_ORGANIZATION", "TF_CLOUD_HOSTNAME"):
        assert gone not in env
    assert env["TF_LOG"] == "DEBUG"


def test_terraform_cli_config_is_a_managed_file_never_the_users():
    inherited = {"TF_CLI_CONFIG_FILE": str(Path.home() / ".terraformrc")}
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    assert env["TF_CLI_CONFIG_FILE"] == str(sealed.CACHE / "sealed.tfrc")


def test_terraform_version_check_is_disabled():
    inherited = {"CHECKPOINT_DISABLE": ""}
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    assert env["CHECKPOINT_DISABLE"] == "1"


def test_terraform_plugin_cache_defaults_to_the_repo_cache():
    inherited = {"TF_PLUGIN_CACHE_DIR": "/elsewhere"}
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    assert env["TF_PLUGIN_CACHE_DIR"] == str(sealed.REPO / ".cache" / "terraform-plugins")


def test_terraform_plugin_cache_override_is_made_absolute(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    inherited = {"SEALED_TF_PLUGIN_CACHE": "plugins"}
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    assert env["TF_PLUGIN_CACHE_DIR"] == str(tmp_path / "plugins")


def test_prepare_terraform_empties_the_config_and_creates_the_cache(tmp_path):
    config = tmp_path / "cache" / "sealed.tfrc"
    config.parent.mkdir()
    config.write_text('credentials "app.terraform.io" { token = "tok" }\n')
    plugins = tmp_path / "plugins"
    sealed.prepare_terraform({"TF_CLI_CONFIG_FILE": str(config), "TF_PLUGIN_CACHE_DIR": str(plugins)})
    assert config.read_bytes() == b""
    assert plugins.is_dir()
    assert (config.parent / ".gitignore").read_text() == "*\n"


def test_prepare_terraform_names_an_unwritable_config_directory(tmp_path):
    blocker = tmp_path / "cache"
    blocker.write_text("a file, not a directory")
    env = {"TF_CLI_CONFIG_FILE": str(blocker / "sealed.tfrc"), "TF_PLUGIN_CACHE_DIR": str(tmp_path / "plugins")}
    with pytest.raises(sealed.SealError, match="Make that directory writable") as e:
        sealed.prepare_terraform(env)
    assert str(blocker) in str(e.value)
    assert "SEALED_TF_PLUGIN_CACHE" not in str(e.value)


def test_replacing_an_inherited_plugin_cache_is_reported():
    inherited = {"TF_PLUGIN_CACHE_DIR": "/elsewhere"}
    env = sealed.sealed_env(inherited, "http://127.0.0.1:4566", "us-east-1", "test")
    notice = sealed.plugin_cache_notice(inherited, env)
    assert notice is not None
    assert "/elsewhere" in notice
    assert "SEALED_TF_PLUGIN_CACHE" in notice


def test_an_inherited_plugin_cache_equal_to_the_sealed_one_is_not_reported():
    env = sealed.sealed_env({}, "http://127.0.0.1:4566", "us-east-1", "test")
    assert sealed.plugin_cache_notice({}, env) is None
    inherited = {"TF_PLUGIN_CACHE_DIR": env["TF_PLUGIN_CACHE_DIR"]}
    assert sealed.plugin_cache_notice(inherited, env) is None


def test_print_env_seals_terraform_too():
    inherited = {"TF_TOKEN_app_terraform_io": "tok", "TF_LOG": "DEBUG"}
    env = sealed.sealed_env(inherited, "http://localhost:4566", "us-east-1", "test")
    out = sealed.shell_exports(inherited, env).splitlines()
    assert "unset TF_TOKEN_app_terraform_io" in out
    assert f"export TF_CLI_CONFIG_FILE={env['TF_CLI_CONFIG_FILE']}" in out
    assert f"export TF_PLUGIN_CACHE_DIR={env['TF_PLUGIN_CACHE_DIR']}" in out
    assert not any("TF_LOG" in line for line in out)


def run(*args, env=None):
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        check=False,
        capture_output=True,
        text=True,
        env={**os.environ, **(env or {})},
    )


def test_command_runs_with_sealed_environment_and_its_exit_status():
    r = run("sh", "-c", 'echo "$AWS_ENDPOINT_URL $AWS_PROFILE"; exit 7', env={"AWS_PROFILE": "prod"})
    assert r.returncode == 7
    assert r.stdout.strip() == "http://127.0.0.1:4566"


def test_command_runs_with_an_empty_terraform_config_and_an_existing_plugin_cache(tmp_path):
    r = run(
        "sh",
        "-c",
        'test -d "$TF_PLUGIN_CACHE_DIR" && test ! -s "$TF_CLI_CONFIG_FILE" && echo "$TF_PLUGIN_CACHE_DIR $TF_TOKEN_x"',
        env={"SEALED_TF_PLUGIN_CACHE": str(tmp_path / "plugins"), "TF_TOKEN_x": "tok"},
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == str(tmp_path / "plugins")


def test_unwritable_plugin_cache_exits_2_without_running_the_command(tmp_path):
    blocker = tmp_path / "f"
    blocker.write_text("a file, not a directory")
    r = run("sh", "-c", "echo ran", env={"SEALED_TF_PLUGIN_CACHE": str(blocker / "x")})
    assert r.returncode == 2
    assert "ran" not in r.stdout
    assert str(blocker / "x") in r.stderr
    assert "SEALED_TF_PLUGIN_CACHE" in r.stderr


def test_inherited_plugin_cache_is_replaced_with_a_note(tmp_path):
    r = run(
        "sh",
        "-c",
        'echo "$TF_PLUGIN_CACHE_DIR"',
        env={"TF_PLUGIN_CACHE_DIR": str(tmp_path / "theirs"), "SEALED_TF_PLUGIN_CACHE": str(tmp_path / "ours")},
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == str(tmp_path / "ours")
    assert f"ignoring TF_PLUGIN_CACHE_DIR={tmp_path / 'theirs'}" in r.stderr


def test_help_lists_the_environment_settings():
    r = run("--help")
    assert r.returncode == 0
    for name in ("SEALED_ENDPOINT", "SEALED_REGION", "SEALED_ACCESS_KEY_ID", "SEALED_TF_PLUGIN_CACHE"):
        assert name in r.stdout


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
