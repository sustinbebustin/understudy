# understudy in docker compose

understudy as a compose service next to an application container. The app is a small Go program using `aws-sdk-go-v2`. It stores a note in S3, queues a job for it on SQS, takes the job back off the queue, reads the note and counts the notes in the bucket. understudy keeps its state in a named volume, so the count grows across restarts.

| Path | What it is |
|---|---|
| `compose.yaml` | The two services, commented: what to publish, how the app finds understudy, where state is kept |
| `app/` | The application and its Dockerfile |
| `demo.sh` | The commands below, in one script, with a check that the state survived a restart |

What the compose file shows:

- **Endpoint.** The app reaches understudy by its service name, `AWS_ENDPOINT_URL=http://understudy:4566`. No other AWS setting changes, apart from path-style S3 (below).
- **Readiness.** The image's healthcheck runs `understudy health`, so `depends_on: condition: service_healthy` and `docker compose up --wait` start the app only once understudy answers.
- **Exposure.** The port is published on `127.0.0.1` only. Inside its container understudy listens on every interface without an admin token, so the published address is what keeps it private to your machine.
- **Persistence.** `UNDERSTUDY_PERSIST=/data` on a named volume keeps buckets, queues and messages across `restart` and `down`. `down --volumes` starts you from empty.
- **Path-style S3.** A bucket in the hostname (`compose-demo.understudy`) does not resolve on the compose network, so the Go client sets `UsePathStyle`. boto3 and the AWS CLI use path style with a custom endpoint by themselves; the JavaScript SDK needs `forcePathStyle: true`.

## Run it

You need Docker with the compose plugin, and the AWS CLI for the last step. No release image is published yet, so the first `up` builds understudy's image from this repository (`../..`). It is tagged `understudy:dev`, the same tag `just image` builds.

From this directory:

```sh
./demo.sh
```

It runs these commands, which you can also type one by one:

```sh
docker compose up --build --wait understudy   # build, start, wait until healthy
docker compose run --rm app                   # the app's first run
docker compose restart understudy             # understudy saves its state on shutdown
docker compose up --wait understudy
docker compose run --rm app                   # the second run counts one more note
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
  aws --endpoint-url http://127.0.0.1:4566 s3 ls s3://compose-demo/notes/
docker compose down --volumes                 # stop, and drop the saved state
```

The two app runs print:

```text
stored s3://compose-demo/notes/20261013T145550.517619326Z.txt and queued a job for it on http://understudy:4566/000000000000/compose-demo-jobs
took the job for notes/20261013T145550.517619326Z.txt off the queue; the note says "hello from the app container"
the bucket holds 1 note(s)
...
the bucket holds 2 note(s)
```

The queue URL names `understudy:4566` because understudy builds queue URLs from the host the request was sent to, so the URL works from wherever the caller is.

`KEEP=1 ./demo.sh` leaves the stack running. If port 4566 is taken on your machine, set `UNDERSTUDY_PORT` to another port, or to `0` to let Docker pick one; `docker compose port understudy 4566` prints the one in use.

## Using it in your project

Copy `compose.yaml`. Then:

- Replace the `app` service with your own, keeping its `AWS_*` environment and `depends_on`.
- Once you have built understudy's image, delete the `build:` section under `understudy`.
- To run Lambda functions or DynamoDB from compose, understudy needs the Docker socket. Add this to the `understudy` service. For DynamoDB, also set `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE: "1"` to accept the DynamoDB Local licence.

  ```yaml
      volumes:
        - understudy-data:/data
        - /var/run/docker.sock:/var/run/docker.sock
      group_add:
        # The socket's group, so understudy's non-root user can use it:
        # DOCKER_GID=$(stat -c %g /var/run/docker.sock) docker compose up
        - "${DOCKER_GID:?set DOCKER_GID to the group of /var/run/docker.sock}"
  ```

  understudy notices that it runs in a container and attaches function containers to the compose network, so code inside a function reaches it too.
