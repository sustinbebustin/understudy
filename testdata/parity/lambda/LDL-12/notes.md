date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:); the zip stand-in takes no `-r`, so la.zip and lb.zip were built from `conflict.txt python` instead of `.` (# session:), with the same entries.

- `{"file": "b\n", "module": "b"}` for a-then-b: matches (`lambda.layers.merge-conflict` = `later-overwrites`, 8.1).
- `a` for b-then-a: matches (`{"file": "a\n", "module": "a"}`).
- python3.13 `sys.path` (02): `/var/task, /opt/python/lib/python3.13/site-packages, /opt/python, /var/lang/lib/python3.13/site-packages, /var/runtime, /var/lang/lib/python313.zip, /var/lang/lib/python3.13, /var/lang/lib/python3.13/lib-dynload, /var/lang/lib/python3.13/site-packages, /opt/python/lib/python3.13/site-packages`. Layer directories after /var/task and before /var/runtime and the runtime site-packages: matches 8.2; between the two, `site-packages` comes before `/opt/python`. `/opt` lists `conflict.txt, python`.
- PATH: `/var/lang/bin:/usr/local/bin:/usr/bin/:/bin:/opt/bin` (matches 8.2). LD_LIBRARY_PATH: `/var/lang/lib:/lib64:/usr/lib64:/var/runtime:/var/runtime/lib:/var/task:/var/task/lib:/opt/lib` (matches). PYTHONPATH: `/var/runtime` (matches).
- NODE_PATH nodejs22.x: `/opt/nodejs/node22/node_modules:/opt/nodejs/node_modules:/var/runtime/node_modules:/var/runtime:/var/task`; nodejs24.x: `/opt/nodejs/node24/node_modules:/opt/nodejs/node_modules:/var/runtime/node_modules:/var/runtime:/var/task`. The runtime sets it (11.5 item 5's inference holds); the documented sample (10.4) differs (node12, no /var/runtime, /var/task).
