"""SSM Parameter Store (docs/research/ssm-parameters.md).

Recorded shapes cite LocalStack's AWS-validated snapshots
(localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json).
"""

from __future__ import annotations

import base64
from collections.abc import Callable
from typing import Any

import pytest

from support import REGION, raises, status_of, uniq


@pytest.fixture
def make_param(ssm: Any, cleanup: Any) -> Callable[..., str]:
    def make(name: str, value: str = "v", type_: str = "String", **args: Any) -> str:
        ssm.put_parameter(Name=name, Value=value, Type=type_, **args)
        cleanup.add(ssm.delete_parameter, Name=name)
        return name

    return make


def account(sts: Any) -> str:
    return sts.get_caller_identity()["Account"]


def test_string_put_get_overwrite_and_version(ssm: Any, sts: Any, cleanup: Any) -> None:
    name = "/" + uniq("conf-ssm") + "/string"
    arn = f"arn:aws:ssm:{REGION}:{account(sts)}:parameter{name}"
    put = ssm.put_parameter(Name=name, Value="one", Type="String")
    cleanup.add(ssm.delete_parameter, Name=name)
    # test_ssm.snapshot.json:L19-L26
    assert (put["Tier"], put["Version"]) == ("Standard", 1)
    # test_ssm.snapshot.json:L27-L56: by name and by ARN, no Selector.
    for ask in (name, arn):
        got = ssm.get_parameter(Name=ask)["Parameter"]
        assert (got["Name"], got["Value"], got["Version"], got["ARN"], got["DataType"]) == (name, "one", 1, arn, "text")
        assert "Selector" not in got and "SourceResult" not in got
    # ssm-parameters.md 7.1
    with raises("ParameterAlreadyExists") as info:
        ssm.put_parameter(Name=name, Value="two", Type="String")
    assert status_of(info.value) == 400
    # ssm-parameters.md 6.1, 6.3
    assert ssm.put_parameter(Name=name, Value="two", Overwrite=True)["Version"] == 2
    old = ssm.get_parameter(Name=name + ":1")["Parameter"]
    assert (old["Value"], old["Selector"]) == ("one", ":1")


def test_get_after_delete_is_parameter_not_found(ssm: Any, make_param: Callable[..., str]) -> None:
    name = make_param("/" + uniq("conf-ssm") + "/gone")
    ssm.delete_parameter(Name=name)
    # localstack@8b9a79f tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114
    with raises("ParameterNotFound") as info:
        ssm.get_parameter(Name=name)
    assert status_of(info.value) == 400
    assert info.value.response["Error"]["Message"] == ""


def test_secure_string_with_and_without_decryption(ssm: Any, kms: Any, sts: Any, make_param: Callable[..., str]) -> None:
    name = make_param("/" + uniq("conf-ssm") + "/secure", value="hunter2", type_="SecureString")
    arn = f"arn:aws:ssm:{REGION}:{account(sts)}:parameter{name}"
    assert ssm.get_parameter(Name=name, WithDecryption=True)["Parameter"]["Value"] == "hunter2"
    sealed = ssm.get_parameter(Name=name)["Parameter"]["Value"]
    assert sealed != "hunter2"
    # ssm-parameters.md 5.2: KMS Decrypt with PARAMETER_ARN opens the value.
    opened = kms.decrypt(CiphertextBlob=base64.b64decode(sealed), EncryptionContext={"PARAMETER_ARN": arn})
    assert opened["Plaintext"] == b"hunter2"
    # ssm-parameters.md 5.4: the default key reads back alias/aws/ssm.
    meta = ssm.describe_parameters(ParameterFilters=[{"Key": "Name", "Option": "Equals", "Values": [name]}])
    assert [p.get("KeyId") for p in meta["Parameters"]] == ["alias/aws/ssm"]
    assert "NextToken" not in meta


def test_get_parameters_in_chunks_of_ten_as_powertools(ssm: Any, make_param: Callable[..., str]) -> None:
    # ssm-parameters.md 10.3 [C]: Powertools sends GetParameters in chunks of
    # 10 and indexes InvalidParameters without a default (ssm.py:L671-L685).
    base = "/" + uniq("conf-ssm")
    names = [make_param(f"{base}/p{i:02d}") for i in range(12)]
    wanted = [*names, f"{base}/missing"]
    found: list[str] = []
    invalid: list[str] = []
    for i in range(0, len(wanted), 10):
        resp = ssm.get_parameters(Names=wanted[i : i + 10])
        found += [p["Name"] for p in resp["Parameters"]]
        invalid += resp["InvalidParameters"]
    assert sorted(found) == names
    assert invalid == [f"{base}/missing"]


def test_get_parameters_by_path_recursive_across_pages(ssm: Any, make_param: Callable[..., str]) -> None:
    # ssm-parameters.md 7.4, 10.3: the by-path paginator with Recursive.
    base = "/" + uniq("conf-ssm")
    names = sorted(make_param(f"{base}/l{i % 2}/p{i}") for i in range(5))
    pages = ssm.get_paginator("get_parameters_by_path").paginate(Path=base, Recursive=True, PaginationConfig={"PageSize": 2})
    got = [p["Name"] for page in pages for p in page["Parameters"]]
    assert sorted(got) == names


def test_history_and_labels(ssm: Any, make_param: Callable[..., str]) -> None:
    name = make_param("/" + uniq("conf-ssm") + "/labeled", value="v1")
    ssm.put_parameter(Name=name, Value="v2", Overwrite=True)
    # ssm-parameters.md 6.2: invalid labels are listed, 200.
    label = ssm.label_parameter_version(Name=name, ParameterVersion=1, Labels=["stable", "1bad"])
    assert (label["ParameterVersion"], label["InvalidLabels"]) == (1, ["1bad"])
    got = ssm.get_parameter(Name=name + ":stable")["Parameter"]
    assert (got["Value"], got["Selector"]) == ("v1", ":stable")
    # ssm-parameters.md 7.6: oldest first, Policies [] present.
    hist = ssm.get_parameter_history(Name=name)["Parameters"]
    assert [h["Version"] for h in hist] == [1, 2]
    assert hist[0]["Labels"] == ["stable"]
    assert all(h["Policies"] == [] for h in hist)
    unlabel = ssm.unlabel_parameter_version(Name=name, ParameterVersion=1, Labels=["stable", "absent"])
    assert (unlabel["RemovedLabels"], unlabel["InvalidLabels"]) == (["stable"], ["absent"])


def test_tags(ssm: Any, make_param: Callable[..., str]) -> None:
    # ssm-parameters.md 7.9
    name = make_param("/" + uniq("conf-ssm") + "/tagged", Tags=[{"Key": "env", "Value": "dev"}])
    ssm.add_tags_to_resource(ResourceType="Parameter", ResourceId=name, Tags=[{"Key": "env", "Value": "prod"}, {"Key": "team", "Value": "a"}])
    ssm.remove_tags_from_resource(ResourceType="Parameter", ResourceId=name, TagKeys=["team"])
    tags = ssm.list_tags_for_resource(ResourceType="Parameter", ResourceId=name)["TagList"]
    assert tags == [{"Key": "env", "Value": "prod"}]


def test_delete_parameters(ssm: Any, make_param: Callable[..., str]) -> None:
    # ssm-parameters.md 7.7: deleted names alphabetical, missing ones listed.
    base = "/" + uniq("conf-ssm")
    b, a = make_param(f"{base}/b"), make_param(f"{base}/a")
    out = ssm.delete_parameters(Names=[b, f"{base}/missing", a])
    assert out["DeletedParameters"] == [a, b]
    assert out["InvalidParameters"] == [f"{base}/missing"]
