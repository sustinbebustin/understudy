package iam

import "github.com/sustinbebustin/understudy/internal/policycatalogue"

// Options are the IAM settings.
type Options struct {
	// Catalogue loads the AWS managed policies IAM serves; nil serves none,
	// so every arn:aws:iam::aws:policy ARN is NoSuchEntity. IAM calls it
	// only for a call that names an AWS managed policy ARN or lists AWS
	// managed policies.
	Catalogue policycatalogue.Loader
}
