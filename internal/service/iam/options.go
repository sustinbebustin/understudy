package iam

import "github.com/sustinbebustin/understudy/internal/policycatalogue"

// Options are the IAM settings.
type Options struct {
	// Catalogue loads the AWS managed policies IAM serves; nil serves none,
	// so every arn:aws:iam::aws:policy ARN is NoSuchEntity. Not used yet:
	// IAM still accepts any well-formed AWS managed policy ARN.
	Catalogue policycatalogue.Loader
}
