package policydoc

import (
	"slices"
	"strconv"
	"strings"
)

// checkLegacyDates applies IAM's legacy parsing to the values of every Date
// condition: an ISO 8601 date and time, or a non-negative epoch number. A
// boolean fails; a number is checked as its literal text.
func checkLegacyDates(statements []Statement) *ValidationError {
	for _, st := range statements {
		for _, cl := range st.Condition {
			if cl.Operator.Test < DateEquals || cl.Operator.Test > DateGreaterThanEquals {
				continue
			}
			for _, v := range cl.Values {
				var text string
				switch x := v.(type) {
				case StringValue:
					text = string(x)
				case NumberValue:
					text = string(x)
				case BoolValue:
					return &ValidationError{Rule: RuleLegacy, Subject: strconv.FormatBool(bool(x))}
				}
				if !legacyDateOK(text) {
					return &ValidationError{Rule: RuleLegacy, Subject: text}
				}
			}
		}
	}
	return nil
}

// legacyDateOK is moto's date check: a value with a "t" or a "-" is a date
// and time, any other an epoch number.
func legacyDateOK(s string) bool {
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "t") && !strings.Contains(s, "-") {
		return intIn(s, 0, 1<<63-1)
	}
	datePart, timePart, _ := strings.Cut(lower, "t")
	negative := strings.HasPrefix(datePart, "-")
	dateParts := strings.Split(strings.TrimPrefix(datePart, "-"), "-")
	year := dateParts[0]
	if negative {
		year = "-" + year
	}
	switch {
	case !intIn(year, -292275054, 292278993),
		len(dateParts) > 1 && !intIn(dateParts[1], 1, 12),
		len(dateParts) > 2 && !intIn(dateParts[2], 1, 31),
		len(dateParts) > 3:
		return false
	}
	timeParts := strings.Split(timePart, ":")
	switch {
	case timeParts[0] != "" && !intIn(timeParts[0], 0, 23),
		len(timeParts) > 1 && !intIn(timeParts[1], 0, 59):
		return false
	case len(timeParts) > 2:
		return legacySecondsOK(timeParts[2])
	default:
		return true
	}
}

// legacySecondsOK checks the seconds element with its optional fraction and
// zone: "z", or an offset whose hours are the two digits after the sign
// (the offset minutes are the next ":" element, which moto never checks).
// moto accepts only "+" offsets; "-" is accepted too, because IAM documents
// ISO 8601 and real offsets west of UTC are negative.
func legacySecondsOK(s string) bool {
	seconds := s
	if before, after, ok := strings.Cut(s, "z"); ok {
		if after != "" {
			return false
		}
		seconds = before
	} else if i := strings.IndexAny(s, "+-"); i >= 0 {
		hours := s[i+1:]
		if len(hours) != 2 || !intIn(hours, 0, 23) {
			return false
		}
		seconds = s[:i]
	}
	whole, fraction, hasFraction := strings.Cut(seconds, ".")
	return intIn(whole, 0, 59) && (!hasFraction || intIn(fraction, 0, 999999999))
}

// intIn reports whether s is a decimal integer in [lo, hi], read the way
// Python's int() reads it for these checks: surrounding space and a sign
// allowed.
func intIn(s string, lo, hi int64) bool {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return err == nil && lo <= n && n <= hi
}

// checkResourceFormats applies IAM's resource stage the way moto does: the
// Resource values of every statement, then the NotResource values. moto
// keeps the last format problem it finds, so a later statement's problem
// replaces an earlier one; format is returned for IAM to report after the
// actions-required check. Until a format problem is found, each
// statement's values also go through the legacy check, whose failure is
// returned as legacy and outranks everything after the version check.
func checkResourceFormats(statements []Statement) (legacy, format *ValidationError) {
	for _, not := range []bool{false, true} {
		for _, st := range statements {
			values, ok := resourceValues(st.Resource, not)
			if !ok {
				continue
			}
			if err := listFormatError(values); err != nil {
				format = err
			}
			if format != nil {
				continue
			}
			for _, r := range values {
				if !legacyResourceOK(r) {
					return &ValidationError{Rule: RuleLegacy, Subject: r}, nil
				}
			}
		}
	}
	return nil, format
}

// listFormatError returns the format problem moto keeps for one list: it
// checks the values in descending order and keeps the last problem, which
// is the problem of the least value in sort order.
func listFormatError(values []string) *ValidationError {
	ascending := slices.Clone(values)
	slices.Sort(ascending)
	for _, r := range ascending {
		if err := resourceFormatError(r); err != nil {
			return err
		}
	}
	return nil
}

// resourceValues returns the Resource values, or the NotResource values when
// not is set, and whether the statement has that element.
func resourceValues(r Resources, not bool) ([]string, bool) {
	switch x := r.(type) {
	case Resource:
		return x, !not
	case NotResource:
		return x, not
	default:
		return nil, false
	}
}

// resourceFormatError is moto's resource format check. The first element
// is not required to be "arn".
func resourceFormatError(r string) *ValidationError {
	if r == "*" {
		return nil
	}
	_, rest, ok := strings.Cut(r, ":")
	if !ok {
		return &ValidationError{Rule: RuleResourceARN, Subject: r}
	}
	partition, rest, ok := strings.Cut(rest, ":")
	if partition != "*" && !knownPartition(partition) {
		return &ValidationError{Rule: RuleResourcePartition, Subject: r}
	}
	if !ok {
		return &ValidationError{Rule: RuleResourceVendor, Subject: r}
	}
	service, regionRest, _ := strings.Cut(rest, ":")
	_, accountRest, _ := strings.Cut(regionRest, ":")
	_, resourceID, _ := strings.Cut(accountRest, ":")
	hasRegion := !strings.HasPrefix(regionRest, ":")
	switch {
	case service == "iam" && hasRegion:
		return &ValidationError{Rule: RuleIAMResourceRegion, Subject: r}
	case service == "s3" && hasRegion && !strings.HasPrefix(resourceID, "accesspoint/"):
		return &ValidationError{Rule: RuleS3ResourceRegion, Subject: r}
	case service == "iam" && resourceID != "*" && !slices.ContainsFunc(iamResourcePaths(), func(p string) bool { return strings.HasPrefix(resourceID, p) }):
		return &ValidationError{Rule: RuleIAMResourcePath, Subject: r}
	default:
		return nil
	}
}

// legacyResourceOK is moto's legacy resource check: a value with "::" has
// all six elements, and the service element is not empty.
func legacyResourceOK(r string) bool {
	if r == "*" {
		return true
	}
	parts := strings.Split(r, ":")
	return (len(parts) >= 6 || !strings.Contains(r, "::")) && len(parts) > 2 && parts[2] != ""
}

// knownPartition reports whether p is a partition moto knows.
func knownPartition(p string) bool {
	switch p {
	case "aws", "aws-cn", "aws-us-gov", "aws-iso", "aws-iso-b", "aws-eusc":
		return true
	default:
		return false
	}
}

// iamResourcePaths are the resource types an IAM resource may start with.
func iamResourcePaths() []string {
	return []string{
		"user/", "federated-user/", "role/", "group/", "instance-profile/", "mfa/", "server-certificate/",
		"policy/", "sms-mfa/", "saml-provider/", "oidc-provider/", "report/", "access-report/",
	}
}

// partitionMessage rebuilds the six-element resource moto names in its
// unknown-partition message, with "*" for missing elements.
func partitionMessage(r string) string {
	_, rest, _ := strings.Cut(r, ":")
	partition, rest, _ := strings.Cut(rest, ":")
	parts := strings.Split(rest, ":")
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return "*"
	}
	service := at(0)
	if service == "" && len(parts) == 1 {
		service = "*"
	}
	last := "*"
	if len(parts) > 3 {
		last = strings.Join(parts[3:], ":")
	}
	// Concatenated rather than %q-formatted: moto writes the text unescaped.
	arn := strings.Join([]string{"arn", partition, service, at(1), at(2), last}, ":")
	return `Partition "` + partition + `" is not valid for resource "` + arn + `".`
}
