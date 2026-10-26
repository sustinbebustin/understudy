# AWS Managed Policy Reference Guide

Synthetic copyright banner of the parser fixture.

Synthetic trademark paragraph of the parser fixture.

## Table of contents
+ [What are AWS managed policies?](#about-managed-policy-reference)
+ [ExampleAdminAccess](#ExampleAdminAccess)
+ [ExampleDataScientist](#ExampleDataScientist)
+ [ExampleQueueReader](#ExampleQueueReader)
+ [ExampleServiceLinkedRolePolicy](#ExampleServiceLinkedRolePolicy)

## What are AWS managed policies?
<a name="about-managed-policy-reference"></a>

Synthetic about page. It mentions **Type**: and **Policy version:** in prose, which open no policy.

### Deprecated AWS managed policies
<a name="about-managed-policy-reference-deprecated"></a>

Synthetic list of deprecated policies.

## ExampleAdminAccess
<a name="ExampleAdminAccess"></a>

**Description**: Grants every example permission. See the [example guide](https://example.com/guide?a=1&b=2) and `notes` <here>.

`ExampleAdminAccess` is a synthetic policy of the parser fixture.

### Using this policy
<a name="ExampleAdminAccess-how-to-use"></a>

Synthetic usage text.

### Policy details
<a name="ExampleAdminAccess-details"></a>
+ **Type**: AWS managed policy
+ **Creation time**: February 06, 2015, 18:39 UTC
+ **Edited time:** March 04, 2024, 10:05 UTC
+ **ARN**: `arn:aws:iam::aws:policy/ExampleAdminAccess`

### Policy version
<a name="ExampleAdminAccess-version"></a>

**Policy version:** v3 (default)

Synthetic version text.

### JSON policy document
<a name="ExampleAdminAccess-json"></a>

```
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "sqs:*",
        "sns:*"
      ],
      "Resource" : "*"
    }
  ]
}
```

### Learn more
<a name="ExampleAdminAccess-learn-more"></a>
+ [Synthetic link](https://example.com/learn-more)

## ExampleDataScientist
<a name="ExampleDataScientist"></a>

**Description**: Synthetic job function policy for data scientists.

`ExampleDataScientist` is a synthetic policy of the parser fixture.

### Using this policy
<a name="ExampleDataScientist-how-to-use"></a>

Synthetic usage text.

### Policy details
<a name="ExampleDataScientist-details"></a>
+ **Type**: Job function policy
+ **Creation time**: November 10, 2016, 17:02 UTC
+ **Edited time:** October 01, 2026, 00:00 UTC
+ **ARN**: `arn:aws:iam::aws:policy/job-function/ExampleDataScientist`

### Policy version
<a name="ExampleDataScientist-version"></a>

**Policy version:** v190 (default)

Synthetic version text.

### JSON policy document
<a name="ExampleDataScientist-json"></a>

```
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Sid" : "ReadNotebooks",
      "Effect" : "Allow",
      "Action" : "s3:GetObject",
      "Resource" : "arn:aws:s3:::example-notebooks/*",
      "Condition" : {
        "StringLike" : {
          "aws:username" : "data+science"
        }
      }
    }
  ]
}
```

### Learn more
<a name="ExampleDataScientist-learn-more"></a>
+ [Synthetic link](https://example.com/learn-more)

## ExampleQueueReader
<a name="ExampleQueueReader"></a>

**Description**: Lets a worker read and delete example queue messages.

`ExampleQueueReader` is a synthetic policy of the parser fixture.

### Using this policy
<a name="ExampleQueueReader-how-to-use"></a>

Synthetic usage text.

### Policy details
<a name="ExampleQueueReader-details"></a>
+ **Type**: Service role policy
+ **Creation time**: April 09, 2015, 15:03 UTC
+ **Edited time:** April 09, 2015, 15:03 UTC
+ **ARN**: `arn:aws:iam::aws:policy/service-role/ExampleQueueReader`

### Policy version
<a name="ExampleQueueReader-version"></a>

**Policy version:** v1 (default)

Synthetic version text.

### JSON policy document
<a name="ExampleQueueReader-json"></a>

```
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Action" : [
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage"
      ],
      "Effect" : "Allow",
      "Resource" : "*"
    }
  ]
}
```

### Learn more
<a name="ExampleQueueReader-learn-more"></a>
+ [Synthetic link](https://example.com/learn-more)

## ExampleServiceLinkedRolePolicy
<a name="ExampleServiceLinkedRolePolicy"></a>

**Description**: Lets the example service manage its log groups.

`ExampleServiceLinkedRolePolicy` is a synthetic policy of the parser fixture.

### Using this policy
<a name="ExampleServiceLinkedRolePolicy-how-to-use"></a>

Synthetic usage text.

### Policy details
<a name="ExampleServiceLinkedRolePolicy-details"></a>
+ **Type**: Service-linked role policy
+ **Creation time**: January 31, 2019, 23:59 UTC
+ **Edited time:** June 15, 2022, 08:30 UTC
+ **ARN**: `arn:aws:iam::aws:policy/aws-service-role/ExampleServiceLinkedRolePolicy`

### Policy version
<a name="ExampleServiceLinkedRolePolicy-version"></a>

**Policy version:** v12 (default)

Synthetic version text.

### JSON policy document
<a name="ExampleServiceLinkedRolePolicy-json"></a>

```
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : "logs:PutLogEvents",
      "Resource" : "arn:aws:logs:*:*:log-group:/example/*"
    }
  ]
}
```

### Learn more
<a name="ExampleServiceLinkedRolePolicy-learn-more"></a>
+ [Synthetic link](https://example.com/learn-more)
