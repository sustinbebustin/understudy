"""A stand-in for pyshorteners in the s3-lambda-ses-terraform pattern.

The upstream layer bundles pyshorteners (GPLv3) and certifi (MPL-2.0), which
this repository cannot carry; see ../../../ADJUSTMENTS.md. emailSender.py
imports this module when it loads and calls it only on its URL-shortener
path, which needs the network. The suite never takes that path, so a call
means the run went somewhere it should not.
"""


class Shortener:
    def __init__(self, *args, **kwargs):
        raise RuntimeError(
            "pyshorteners is a stub in understudy's pattern suite: the URL-shortener "
            "path calls an outside service and is not exercised; upload an object "
            "of 25 MB or less so the function attaches it instead"
        )
