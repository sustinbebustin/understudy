"""A helper the Terraform conformance suite ships as a Lambda layer.

Lambda extracts the layer under /opt, and the Python runtime finds modules
in /opt/python.
"""


def greeting(name):
    return "hello, " + name
