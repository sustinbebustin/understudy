# The container-image worker: the Python worker plus a dispatcher for
# scheduled and function URL events. Built by the suite with the functions
# directory as its context.
FROM public.ecr.aws/lambda/python:3.13
COPY pyworker/handler.py imageworker/app.py ${LAMBDA_TASK_ROOT}/
CMD ["app.handler"]
