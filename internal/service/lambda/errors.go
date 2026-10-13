package lambda

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
)

// The message builders over the generated constructors. Texts are the ones
// recorded from AWS (lambda-api.md 2.1 to 7, lambda-events.md 1.3, and the
// LocalStack snapshots they cite) unless a comment says otherwise. Recorded
// client errors of modeled shapes carry Type User; the unmodeled
// ValidationException, AccessDeniedException and
// RequestEntityTooLargeException carry no Type, nor do the few modeled
// errors recorded without one.

// user adds Lambda's Type member of a client error (error-matrix.md 5.6).
func user(e *awserr.Error) *awserr.Error {
	e.Fields = append(e.Fields, awserr.Field{Name: "Type", Value: "User"})
	return e
}

func errFunctionNotFound(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Function not found: " + arn))
}

// errResourceNotFound is the not-found text of function URL configurations,
// event source mappings and a function without a policy.
func errResourceNotFound() *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("The resource you requested does not exist."))
}

// errURLFunctionMissing is CreateFunctionUrlConfig's, UpdateFunctionUrlConfig's
// and ListFunctionUrlConfigs' answer for a missing function or alias.
func errURLFunctionMissing() *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Function does not exist"))
}

func errNotReachable(from, here string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Functions from '" + from + "' are not reachable in this region ('" + here + "')"))
}

// errOtherAccount is recorded with neither a message nor a Type.
func errOtherAccount() *awserr.Error {
	return lambdaapi.ErrAccessDeniedException("")
}

func errQualifierMismatch() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The derived qualifier from the function name does not match the specified qualifier."))
}

// errQualifierNotAccepted is understudy's own text for a qualified name
// passed to an operation that only changes $LATEST or the function, whose
// AWS answer is unrecorded.
func errQualifierNotAccepted(op string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException(op + " applies to the unqualified function; pass the function name or its unqualified ARN without a version or alias."))
}

func errFunctionExists(name string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("Function already exist: " + name))
}

func errUpdateInProgress(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("The operation cannot be performed at this time. An update is in progress for resource: " + arn))
}

// errFunctionState answers an operation the function's State does not
// allow: Pending, or Failed. The spelling is the one LocalStack and the
// client retry matchers share for Pending; AWS's own wording is
// unrecorded (lambda-api.md 5.2).
func errFunctionState(state lambdaapi.State) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("The operation cannot be performed at this time. The function is currently in the following state: " + string(state)))
}

func errRevisionMismatch() *awserr.Error {
	return user(lambdaapi.ErrPreconditionFailedException("The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id"))
}

func errPolicyRevisionMismatch() *awserr.Error {
	return user(lambdaapi.ErrPreconditionFailedException("The Revision Id provided does not match the latest Revision Id. Call the GetPolicy API to retrieve the latest Revision Id"))
}

func errCodeSha256Mismatch(given, actual string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("CodeSHA256 (" + given + ") is different from current CodeSHA256 in $LATEST (" + actual + "). Please try again with the CodeSHA256 in $LATEST."))
}

func errLatestNotDeletable() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("$LATEST version cannot be deleted without deleting the function."))
}

// errAliasNotDeletable is DeleteFunction's answer to an alias qualifier
// (test_function_name_and_qualifier_validation[qualifier_too_long1-delete_function]).
func errAliasNotDeletable() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Deletion of aliases is not currently supported."))
}

// errVersionAliased is understudy's text for deleting a version an alias
// points to, which AWS refuses ("You can't delete a version that an alias
// references", lambda-api.md 6.4) with an unrecorded message.
func errVersionAliased(version, alias string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("Version " + version + " is referenced by alias " + alias + "; point the alias at another version or delete it first."))
}

func errRuntime(value string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Value " + value + " at 'runtime' failed to satisfy constraint: Member must satisfy enum value set: [" +
		strings.Join(listedRuntimes(), ", ") + "] or be a valid ARN"))
}

func errRuntimeDeprecated(runtime string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The runtime parameter of " + runtime +
		" is no longer supported for creating or updating AWS Lambda functions. We recommend you use a supported runtime while creating or updating functions."))
}

func errNameTooLong(name string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException(awserr.CoralMessage([]awserr.Violation{{
		Path: awserr.Path{"functionName"}, Value: awserr.RenderedText{Text: name}, Constraint: awserr.LengthMax{Max: maxNameLength},
	}})))
}

func errEnvironmentTooLarge(measured string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Lambda was unable to configure your environment variables because the environment variables you have provided exceeded the 4KB limit. String measured: " + measured))
}

// errEnvironmentReserved carries the text AWS users report; it is
// unrecorded in the research corpus (lambda-api.md 4.1, Open questions).
func errEnvironmentReserved(keys []string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: " +
		strings.Join(keys, ",")))
}

func errTooManyLayers() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Cannot reference more than 5 layers."))
}

// errLayerNotFound answers every layer reference: understudy has no layers.
func errLayerNotFound(arn string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Layer version " + arn + " does not exist."))
}

func errInvalidZip() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Could not unzip uploaded file. Please check your file, then try to upload again."))
}

func errUnzippedTooLarge() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Unzipped size must be smaller than " + strconv.FormatInt(maxUnzipped, 10) + " bytes"))
}

// errTooManyEntries is understudy's own bound, not AWS's: Lambda documents
// no file count limit, but every entry costs memory to parse and an inode
// to extract (codestore.DefaultMaxEntries).
func errTooManyEntries(headers int64) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The deployment package has " + strconv.FormatInt(headers, 10) +
		" central directory headers, above understudy's limit of " + strconv.FormatInt(codestore.DefaultMaxEntries, 10) +
		" entries (a zip stored uncompressed inside the package counts its entries too). Package fewer files."))
}

// errImageURI answers an ImageUri the Docker Engine would not accept as an
// image reference; the text is inferred.
func errImageURI(bad *docker.InvalidReferenceError) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Source image " + bad.Ref + " is not valid: " + bad.Reason + ". Provide a valid source image."))
}

func errZippedTooLarge() *awserr.Error {
	return lambdaapi.ErrRequestEntityTooLargeException("Zipped size must be smaller than " + strconv.FormatInt(maxZipped, 10) + " bytes")
}

func errImageURIOnZip() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Please don't provide ImageUri when updating a function with packageType Zip."))
}

func errImageURIMissing() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Please provide ImageUri when updating a function with packageType Image."))
}

// errZipNeedsRuntime and errImageTakesNoRuntime carry the texts AWS users
// report for CreateFunction; both are unrecorded in the research corpus.
func errZipNeedsRuntime() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Runtime and Handler are mandatory parameters for functions created with deployment packages."))
}

func errImageTakesNoRuntime() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Please don't provide Handler or Runtime or Layer when the intended function PackageType is Image."))
}

// errCodeSource is understudy's text for code that names no source, or a
// source of the wrong package type, at creation; AWS's is unrecorded.
func errCodeSource(packageType string) *awserr.Error {
	if packageType == string(lambdaapi.PackageTypeImage) {
		return user(lambdaapi.ErrInvalidParameterValueException("A function with PackageType Image needs Code.ImageUri and no ZipFile or S3 location."))
	}
	return user(lambdaapi.ErrInvalidParameterValueException("A function with PackageType Zip needs Code.ZipFile or Code.S3Bucket and Code.S3Key, and no ImageUri."))
}

// errS3Package answers a package S3 would not hand over, naming S3's error
// (AWS-recorded, lambda-api.md 4.3: "Error occurred while GetObject. S3
// Error Code: NoSuchBucket. S3 Error Message: The specified bucket does
// not exist").
func errS3Package(e *awserr.Error) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Error occurred while GetObject. S3 Error Code: " + e.ID.QueryCode + ". S3 Error Message: " + e.Message))
}

// errInvalidMarker is understudy's text for a Marker no listing page
// returned; AWS's is unrecorded.
func errInvalidMarker(marker string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The marker " + marker + " is not one a previous page returned; start the listing again without a Marker."))
}

// errNoPublishedVersion answers Invoke on $LATEST.PUBLISHED, the version
// of Lambda Managed Instances, which understudy does not implement. The
// code is the documented one for a function without that version
// (API_Invoke); the text is understudy's own.
func errNoPublishedVersion(arn string) *awserr.Error {
	return user(lambdaapi.ErrNoPublishedVersionException("The function " + arn + " has no $LATEST.PUBLISHED version: only Lambda Managed Instances functions publish one, and understudy does not implement them. Invoke $LATEST, a version number or an alias."))
}

func errTooManyTags() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Number of tags exceeds resource tag limit."))
}

func errEmptyTags() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("An error occurred and the request cannot be processed."))
}

func errTagsOnQualified() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Tags on function aliases and versions are not supported. Please specify a function ARN."))
}

func errMappingNotFoundARN(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Event source mapping not found: " + arn))
}

func errAliasExists(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("Alias already exists: " + arn))
}

func errAliasNotFound(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Cannot find alias arn: " + arn))
}

// errAliasNotFoundForUpdate is UpdateAlias's spelling.
func errAliasNotFoundForUpdate(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Alias not found: " + arn))
}

func errTooManyWeights() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Number of items in AdditionalVersionWeights cannot be greater than 1"))
}

func errWeightOnPrimary(version string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Invalid function version " + version + ". Function version " + version + " is already included in routing configuration."))
}

// errWeightedLatest is recorded without a Type.
func errWeightedLatest() *awserr.Error {
	return lambdaapi.ErrInvalidParameterValueException("$LATEST is not supported for an alias pointing to more than 1 version")
}

func errStatementExists(sid string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("The statement id (" + sid + ") provided already exists. Please provide a new statement id, or remove the existing statement."))
}

func errLatestPolicy() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("We currently do not support adding policies for $LATEST."))
}

func errInvalidPrincipal() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The provided principal was invalid. Please check the principal and try again."))
}

func errNoPolicy() *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("No policy is associated with the given resource."))
}

func errNoPolicyFound(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("No policy found for: " + arn))
}

func errStatementNotFound(sid string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("Statement " + sid + " is not found in resource policy."))
}

// errPolicyTooLarge carries the text AWS users report; it is unrecorded in
// the research corpus.
func errPolicyTooLarge(size int) *awserr.Error {
	return user(lambdaapi.ErrPolicyLengthExceededException("The final policy size (" + strconv.Itoa(size) + ") is bigger than the limit (" + strconv.Itoa(maxPolicyBytes) + ")."))
}

func errFunctionsOnly() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("This operation is permitted on Lambda functions only. Aliases and versions do not support this operation. Please specify either a function name or an unqualified function ARN."))
}

// errUnreservedMinimum is recorded without a Type.
func errUnreservedMinimum() *awserr.Error {
	return lambdaapi.ErrInvalidParameterValueException("Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [" +
		strconv.Itoa(minUnreservedConcurrency) + "].")
}

func errURLConfigExists(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException("Failed to create function url config for [functionArn = " + arn + "]. Error message:  FunctionUrlConfig exists for this Lambda function"))
}

func errEventInvokeNotFound(arn string) *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("The function " + arn + " doesn't have an EventInvokeConfig"))
}

func errEventInvokeFunctionMissing() *awserr.Error {
	return user(lambdaapi.ErrResourceNotFoundException("The function doesn't exist."))
}

func errEventInvokeNoSettings() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("You must specify at least one of error handling or destination setting."))
}

func errDestinationSelf() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("You can't specify the function as a destination for itself."))
}

func errDestinationMissing(arn string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("The destination ARN " + arn + " is invalid."))
}

func errDestinationConfig(onSuccess, onFailure string) *awserr.Error {
	side := func(name, arn string) string {
		if arn == "" {
			return "null"
		}
		return name + "(destination=" + arn + ")"
	}
	return user(lambdaapi.ErrInvalidParameterValueException("The provided destination config DestinationConfig(onSuccess=" + side("OnSuccess", onSuccess) +
		", onFailure=" + side("OnFailure", onFailure) + ") is invalid."))
}

func errUnrecognizedEventSource() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Unrecognized event source."))
}

// errEventSourceUnsupported answers an event source other than an SQS
// queue: understudy polls only SQS in phase 1.
func errEventSourceUnsupported(arn string) *awserr.Error {
	return lambdaapi.ErrNotImplemented("understudy implements event source mappings for Amazon SQS queues only; " + arn + " is not an SQS queue ARN. Run 'understudy coverage --service lambda' for what is supported.")
}

// publishToRefused is the coverage note of an operation checkPublishTo
// guards.
const publishToRefused = "PublishTo LATEST_PUBLISHED is refused: understudy does not implement Lambda Managed Instances"

// checkPublishTo refuses PublishTo LATEST_PUBLISHED, which publishes the
// $LATEST.PUBLISHED version of a Lambda Managed Instances function;
// understudy does not implement those, and ignoring the member would fake
// a version that never exists.
func checkPublishTo(op string, to *lambdaapi.FunctionVersionLatestPublished) *awserr.Error {
	if to == nil {
		return nil
	}
	return lambdaapi.ErrNotImplemented(op + " with PublishTo " + string(*to) + " publishes the " + latestPublished +
		" version of a Lambda Managed Instances function, which understudy does not implement; nothing was changed. " +
		"Leave PublishTo out, and set Publish (or call PublishVersion) to publish a numbered version.")
}

func errUnsupportedDestination() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Unsupported DestinationConfig parameter for given event source mapping type."))
}

func errBatchWindow() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10"))
}

// errFIFOBatchSize is understudy's text for the documented FIFO batch size
// limit of 10; AWS's text is unrecorded (lambda-events.md 1.3).
func errFIFOBatchSize(size int32) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Batch size " + strconv.Itoa(int(size)) + " is above 10, the maximum for an SQS FIFO queue."))
}

// errPollerConfigs is understudy's text for ScalingConfig together with
// ProvisionedPollerConfig, which the documentation calls mutually
// exclusive; AWS's text is unrecorded (lambda-events.md 1.3).
func errPollerConfigs() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("ScalingConfig and ProvisionedPollerConfig cannot both be set on an event source mapping."))
}

// errTooManyFilters is understudy's text for more filters than the default
// quota of 5 per mapping; AWS's text is unrecorded (lambda-events.md 1.2).
func errTooManyFilters(n int) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("FilterCriteria has " + strconv.Itoa(n) + " filters, above the quota of " +
		strconv.Itoa(maxFilters) + " per event source mapping. Combine patterns or remove filters."))
}

func errInvalidFilter() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Invalid filter pattern definition."))
}

func errMappingFunctionMissing() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Function does not exist"))
}

func errQueueMissing() *awserr.Error {
	return errSQSFailure("AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
}

// errSQSFailure is how Lambda reports an SQS error met while checking a
// mapping's queue: wrapped in its own InvalidParameterValueException, in
// the recorded form of the missing queue.
func errSQSFailure(code, message string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Error occurred while ReceiveMessage. SQS Error Code: " + code + ". SQS Error Message: " + message))
}

func errVisibilityTimeout(visibility, timeout int) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Queue visibility timeout: " + strconv.Itoa(visibility) + " seconds is less than Function timeout: " + strconv.Itoa(timeout) + " seconds"))
}

// errMappingExists keeps the recorded spaces inside the quotes.
func errMappingExists(queue, function, uuid string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException(`An event source mapping with SQS arn (" ` + queue + ` ") and function (" ` + function +
		` ") already exists. Please update or delete the existing mapping with UUID ` + uuid))
}

// errMappingInUse is understudy's text for an update or delete inside a
// transient state under TransitionsClock; the code is AWS's documented one
// (lambda-events.md 1.1), the text unrecorded.
func errMappingInUse(uuid string, state MappingState) *awserr.Error {
	return user(lambdaapi.ErrResourceInUseException("The event source mapping " + uuid + " is " + string(state) + "; retry once it is Enabled or Disabled."))
}
