package e2e_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// The pipeline's fixed names and real AWS settings. The emulator starts
// empty for each run, so fixed names do not collide.
const (
	region        = "us-east-1"
	topicName     = "e2e-orders"
	tapQueueName  = "e2e-tap"
	secretName    = "e2e/pipeline/api"
	lockTable     = "e2e-locks"
	bucketName    = "e2e-pipeline-artifacts"
	traceLogGroup = "/e2e/pipeline/traces"
	keyAlias      = "alias/e2e-pipeline"
	roleName      = "e2e-pipeline-worker"
	// lockTTL is the lock lifetime in seconds after the message was sent.
	lockTTL = 300
	// functionTimeout is every worker's timeout in seconds; each queue's
	// visibility timeout is above it, as Lambda requires.
	functionTimeout = 30

	goWorker    = "go-worker"
	pyWorker    = "py-worker"
	imageWorker = "image-worker"
)

// workerSpec is one worker's queue, subscription and mapping settings.
type workerSpec struct {
	name string
	// visibility is the queue's VisibilityTimeout in seconds.
	visibility int
	// maxReceive is the redrive policy's maxReceiveCount.
	maxReceive int
	batchSize  int32
	window     int32
	// reportFailures sets FunctionResponseTypes ReportBatchItemFailures.
	reportFailures bool
	filterPolicy   string
	// bodyScope sets FilterPolicyScope MessageBody.
	bodyScope bool
	raw       bool
}

var specs = []workerSpec{
	{
		name: goWorker, visibility: 60, maxReceive: 3, batchSize: 10, window: 0, reportFailures: true,
		filterPolicy: `{"kind":["order"]}`, raw: true,
	},
	{
		name: pyWorker, visibility: 120, maxReceive: 5, batchSize: 25, window: 20,
		filterPolicy: `{"priority":[{"numeric":[">=",5]}]}`, bodyScope: true,
	},
	{
		name: imageWorker, visibility: 90, maxReceive: 5, batchSize: 5, window: 0,
		filterPolicy: `{"kind":["refund"],"amount":[{"numeric":[">",100]}]}`, raw: true,
	},
}

func spec(name string) workerSpec {
	for _, s := range specs {
		if s.name == name {
			return s
		}
	}
	panic("no worker named " + name)
}

// pyWindow is the Python worker's batching window, the only worker with
// one.
func pyWindow() time.Duration { return time.Duration(spec(pyWorker).window) * time.Second }

// visibilityOf is worker fn's queue visibility timeout as a duration.
func visibilityOf(fn string) time.Duration { return time.Duration(spec(fn).visibility) * time.Second }

type queue struct{ url, arn string }

// worker is one provisioned worker and its plumbing.
type worker struct {
	spec        workerSpec
	functionARN string
	queue, dlq  queue
	subARN      string
	esmUUID     string
}

// pipeline is everything provision created.
type pipeline struct {
	topicARN    string
	keyARN      string
	roleARN     string
	secretARN   string
	tap         queue
	tapSubARN   string
	workers     map[string]*worker
	functionURL string
}

// artifacts are the built function packages.
type artifacts struct {
	goZip, pyZip []byte
	// image is the local tag of the container-image worker, shaped like the
	// ECR URI AWS requires.
	image string
}

// architecture is the Lambda architecture of this host, so the Go worker
// and the image run natively.
func architecture() lambdatypes.Architecture {
	if runtime.GOARCH == "arm64" {
		return lambdatypes.ArchitectureArm64
	}
	return lambdatypes.ArchitectureX8664
}

// buildArtifacts builds the Go bootstrap, zips both zip workers and builds
// the image. release removes the scratch directory and the image.
func buildArtifacts(ctx context.Context, account, runID string) (artifacts, func(), error) {
	var arts artifacts
	dir, err := os.MkdirTemp("", "e2e-pipeline-")
	if err != nil {
		return arts, func() {}, err
	}
	image := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/understudy-e2e-image-worker:%s", account, region, runID)
	release := func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-pipeline: removing the build directory %s: %v\n", dir, err)
		}
		if err := docker(context.WithoutCancel(ctx), "image", "rm", "--force", image); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-pipeline: removing the worker image: %v; run.sh removes images labeled understudy.dev/e2e-run=%s\n", err, runID)
		}
	}
	bootstrap := filepath.Join(dir, "bootstrap")
	build := exec.CommandContext(ctx, "go", "build", "-tags", "lambda.norpc", "-trimpath", "-o", bootstrap, "./functions/goworker")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return arts, release, fmt.Errorf("go build ./functions/goworker: %w\n%s", err, out)
	}
	if arts.goZip, err = zipFiles(map[string]string{"bootstrap": bootstrap}); err != nil {
		return arts, release, err
	}
	if arts.pyZip, err = zipFiles(map[string]string{"handler.py": "functions/pyworker/handler.py"}); err != nil {
		return arts, release, err
	}
	platform := "linux/" + runtime.GOARCH
	if err := docker(ctx, "build", "--quiet", "--platform", platform,
		"--label", "understudy.dev/test=1", "--label", "understudy.dev/e2e-run="+runID,
		"-t", image, "-f", "functions/imageworker/Dockerfile", "functions"); err != nil {
		return arts, release, err
	}
	arts.image = image
	return arts, release, nil
}

// zipFiles zips name -> path, every entry executable so a bootstrap runs.
func zipFiles(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// provision creates the whole pipeline in dependency order and waits until
// every function is Active and every mapping Enabled. When the caller opted
// DynamoDB out it leaves the lock table out; the tests that make a worker
// take a lock skip (requireDynamoDB), the rest run against the pipeline.
func provision(ctx context.Context, s *suite, arts artifacts) (*pipeline, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	p := &pipeline{workers: map[string]*worker{}}
	steps := []struct {
		name string
		run  func() error
	}{
		{"create the IAM role", func() error { return p.createRole(ctx, s) }},
		{"create the KMS key", func() error { return p.createKey(ctx, s) }},
		{"create the SSE-KMS bucket", func() error { return p.createBucket(ctx, s) }},
		{"create the secret", func() error { return p.createSecret(ctx, s) }},
		{"create the lock table", func() error {
			if dynamoDBOptedOut() {
				return nil
			}
			return createLockTable(ctx, s)
		}},
		{"create the trace log group", func() error {
			_, err := s.logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(traceLogGroup)})
			return err
		}},
		{"create the topic", func() error {
			out, err := s.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
			p.topicARN = aws.ToString(out.TopicArn)
			return err
		}},
		{"create the queues and subscriptions", func() error { return p.createQueues(ctx, s) }},
		{"create the functions", func() error { return p.createFunctions(ctx, s, arts) }},
		{"create the event source mappings", func() error { return p.createMappings(ctx, s) }},
		{"create the function URL", func() error { return p.createFunctionURL(ctx, s) }},
	}
	for _, st := range steps {
		if err := st.run(); err != nil {
			return nil, fmt.Errorf("%s: %w", st.name, err)
		}
	}
	return p, nil
}

func (p *pipeline) createRole(ctx context.Context, s *suite) error {
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	out, err := s.iam.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(trust)})
	if err != nil {
		return err
	}
	p.roleARN = aws.ToString(out.Role.Arn)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueAttributes","secretsmanager:GetSecretValue","dynamodb:PutItem","s3:PutObject","kms:GenerateDataKey","kms:Decrypt","logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`
	_, err = s.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("pipeline"), PolicyDocument: aws.String(policy)})
	return err
}

func (p *pipeline) createKey(ctx context.Context, s *suite) error {
	out, err := s.kms.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("e2e pipeline data key")})
	if err != nil {
		return err
	}
	p.keyARN = aws.ToString(out.KeyMetadata.Arn)
	_, err = s.kms.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(keyAlias), TargetKeyId: out.KeyMetadata.KeyId})
	return err
}

func (p *pipeline) createBucket(ctx context.Context, s *suite) error {
	if _, err := s.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucketName)}); err != nil {
		return err
	}
	_, err := s.s3.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucketName),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{
			ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAwsKms, KMSMasterKeyID: aws.String(p.keyARN)},
			BucketKeyEnabled:                   aws.Bool(true),
		}}},
	})
	return err
}

func (p *pipeline) createSecret(ctx context.Context, s *suite) error {
	out, err := s.secrets.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(secretName), KmsKeyId: aws.String(keyAlias), SecretString: aws.String(`{"token":"t-1"}`),
	})
	if err != nil {
		return err
	}
	p.secretARN = aws.ToString(out.ARN)
	return nil
}

func createLockTable(ctx context.Context, s *suite) error {
	_, err := s.ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(lockTable),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("LockID"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		return err
	}
	if err := dynamodb.NewTableExistsWaiter(s.ddb).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(lockTable)}, 2*time.Minute); err != nil {
		return err
	}
	_, err = s.ddb.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName:               aws.String(lockTable),
		TimeToLiveSpecification: &ddbtypes.TimeToLiveSpecification{AttributeName: aws.String("ExpiresAt"), Enabled: aws.Bool(true)},
	})
	return err
}

// createQueue creates name with attrs and returns its URL and ARN.
func createQueue(ctx context.Context, s *suite, name string, attrs map[string]string) (queue, error) {
	out, err := s.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
	if err != nil {
		return queue{}, err
	}
	q := queue{url: aws.ToString(out.QueueUrl)}
	got, err := s.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: out.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	if err != nil {
		return queue{}, err
	}
	q.arn = got.Attributes["QueueArn"]
	return q, nil
}

// allowTopic lets the topic send to q, as SNS needs in AWS.
func (p *pipeline) allowTopic(ctx context.Context, s *suite, q queue) error {
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":%q,"Condition":{"ArnEquals":{"aws:SourceArn":%q}}}]}`, q.arn, p.topicARN)
	_, err := s.sqs.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(q.url), Attributes: map[string]string{"Policy": policy}})
	return err
}

func (p *pipeline) subscribe(ctx context.Context, s *suite, q queue, attrs map[string]string) (string, error) {
	out, err := s.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(p.topicARN), Protocol: aws.String("sqs"), Endpoint: aws.String(q.arn),
		Attributes: attrs, ReturnSubscriptionArn: true,
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.SubscriptionArn), nil
}

func (p *pipeline) createQueues(ctx context.Context, s *suite) error {
	var err error
	if p.tap, err = createQueue(ctx, s, tapQueueName, nil); err != nil {
		return err
	}
	if err := p.allowTopic(ctx, s, p.tap); err != nil {
		return err
	}
	if p.tapSubARN, err = p.subscribe(ctx, s, p.tap, nil); err != nil {
		return err
	}
	for _, sp := range specs {
		w := &worker{spec: sp}
		if w.dlq, err = createQueue(ctx, s, "e2e-"+sp.name+"-dlq", nil); err != nil {
			return err
		}
		redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":%d}`, w.dlq.arn, sp.maxReceive)
		attrs := map[string]string{"VisibilityTimeout": strconv.Itoa(sp.visibility), "RedrivePolicy": redrive}
		if w.queue, err = createQueue(ctx, s, "e2e-"+sp.name, attrs); err != nil {
			return err
		}
		if err := p.allowTopic(ctx, s, w.queue); err != nil {
			return err
		}
		sub := map[string]string{"FilterPolicy": sp.filterPolicy, "RawMessageDelivery": strconv.FormatBool(sp.raw)}
		if sp.bodyScope {
			sub["FilterPolicyScope"] = "MessageBody"
		}
		if w.subARN, err = p.subscribe(ctx, s, w.queue, sub); err != nil {
			return err
		}
		p.workers[sp.name] = w
	}
	return nil
}

func (p *pipeline) createFunctions(ctx context.Context, s *suite, arts artifacts) error {
	vars := map[string]string{
		"SECRET_ID": secretName, "LOCK_TABLE": lockTable, "BUCKET": bucketName,
		"TRACE_LOG_GROUP": traceLogGroup, "LOCK_TTL_SECONDS": strconv.Itoa(lockTTL),
	}
	base := func(name string) *lambda.CreateFunctionInput {
		return &lambda.CreateFunctionInput{
			FunctionName:  aws.String(name),
			Role:          aws.String(p.roleARN),
			Timeout:       aws.Int32(functionTimeout),
			MemorySize:    aws.Int32(256),
			Architectures: []lambdatypes.Architecture{architecture()},
			Environment:   &lambdatypes.Environment{Variables: vars},
		}
	}
	goIn := base(goWorker)
	goIn.Runtime, goIn.Handler = lambdatypes.RuntimeProvidedal2023, aws.String("bootstrap")
	goIn.Code = &lambdatypes.FunctionCode{ZipFile: arts.goZip}
	pyIn := base(pyWorker)
	pyIn.Runtime, pyIn.Handler = lambdatypes.RuntimePython313, aws.String("handler.handler")
	pyIn.Code = &lambdatypes.FunctionCode{ZipFile: arts.pyZip}
	imgIn := base(imageWorker)
	imgIn.PackageType = lambdatypes.PackageTypeImage
	imgIn.Code = &lambdatypes.FunctionCode{ImageUri: aws.String(arts.image)}
	for _, in := range []*lambda.CreateFunctionInput{goIn, pyIn, imgIn} {
		out, err := s.lambda.CreateFunction(ctx, in)
		if err != nil {
			return fmt.Errorf("%s: %w", aws.ToString(in.FunctionName), err)
		}
		p.workers[aws.ToString(in.FunctionName)].functionARN = aws.ToString(out.FunctionArn)
		if err := lambda.NewFunctionActiveV2Waiter(s.lambda).Wait(ctx, &lambda.GetFunctionInput{FunctionName: in.FunctionName}, 5*time.Minute); err != nil {
			return fmt.Errorf("%s did not become Active: %w", aws.ToString(in.FunctionName), err)
		}
	}
	return nil
}

func (p *pipeline) createMappings(ctx context.Context, s *suite) error {
	for _, sp := range specs {
		w := p.workers[sp.name]
		in := &lambda.CreateEventSourceMappingInput{
			FunctionName:                   aws.String(sp.name),
			EventSourceArn:                 aws.String(w.queue.arn),
			BatchSize:                      aws.Int32(sp.batchSize),
			MaximumBatchingWindowInSeconds: aws.Int32(sp.window),
		}
		if sp.reportFailures {
			in.FunctionResponseTypes = []lambdatypes.FunctionResponseType{lambdatypes.FunctionResponseTypeReportBatchItemFailures}
		}
		out, err := s.lambda.CreateEventSourceMapping(ctx, in)
		if err != nil {
			return fmt.Errorf("%s: %w", sp.name, err)
		}
		w.esmUUID = aws.ToString(out.UUID)
		if err := awaitMappingState(ctx, s, w.esmUUID, "Enabled"); err != nil {
			return fmt.Errorf("%s: %w", sp.name, err)
		}
	}
	return nil
}

// awaitMappingState polls a mapping until it reports state, as the
// Terraform provider's waiter does.
func awaitMappingState(ctx context.Context, s *suite, uuid, state string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		out, err := s.lambda.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(uuid)})
		if err != nil {
			return err
		}
		if aws.ToString(out.State) == state {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("mapping %s stayed %s, want %s", uuid, aws.ToString(out.State), state)
		case <-time.After(pollEvery):
		}
	}
}

func (p *pipeline) createFunctionURL(ctx context.Context, s *suite) error {
	out, err := s.lambda.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{FunctionName: aws.String(imageWorker), AuthType: lambdatypes.FunctionUrlAuthTypeNone})
	if err != nil {
		return err
	}
	p.functionURL = aws.ToString(out.FunctionUrl)
	if _, err := s.lambda.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(imageWorker), StatementId: aws.String("public-url"), Action: aws.String("lambda:InvokeFunctionUrl"),
		Principal: aws.String("*"), FunctionUrlAuthType: lambdatypes.FunctionUrlAuthTypeNone,
	}); err != nil {
		return err
	}
	_, err = s.lambda.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(imageWorker), StatementId: aws.String("public-url-invoke"), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("*"), InvokedViaFunctionUrl: aws.Bool(true),
	})
	return err
}
