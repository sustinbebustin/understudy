package ddblocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AcceptFlag and AcceptEnv are how a user accepts the DynamoDB Local licence
// (ADR 0014). Only the literal value "1" of AcceptEnv accepts.
const (
	AcceptFlag = "--accept-dynamodb-local-license"
	AcceptEnv  = "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"
)

// Licence is whether understudy may pull and run DynamoDB Local.
//
//sumtype:decl
type Licence interface{ isLicence() }

// Disabled is a DynamoDB service the configuration turned off.
type Disabled struct{}

// AwaitingLicence is a licence nobody accepted for the pinned image yet:
// nothing is pulled and DynamoDB calls answer 501 with the Notice.
type AwaitingLicence struct{}

// Accepted is a licence accepted for the image with Digest. Acceptance of
// another digest does not cover the pinned image.
type Accepted struct{ Digest string }

func (Disabled) isLicence()        {}
func (AwaitingLicence) isLicence() {}
func (Accepted) isLicence()        {}

// covers reports whether l lets understudy pull and run the pinned image.
func covers(l Licence) bool {
	a, ok := l.(Accepted)
	return ok && a.Digest == Digest
}

// acceptanceDir holds one file per accepted digest under the data directory.
const acceptanceDir = "dynamodb-local/licence-accepted"

// acceptanceFile is the record of an acceptance of the pinned digest.
func acceptanceFile() string {
	return acceptanceDir + "/" + strings.ReplaceAll(Digest, ":", "-")
}

// LoadLicence returns the licence state for the pinned image. accept is
// AcceptFlag or AcceptEnv given now; with a data directory it is recorded
// per digest, and a recorded acceptance counts on later runs, so the flag
// is needed once per pinned digest. dataDir is nil without persistence. An
// acceptance that cannot be recorded or read is an *AcceptanceError.
func LoadLicence(dataDir *os.Root, accept bool) (Licence, error) {
	switch {
	case accept && dataDir != nil:
		if err := recordAcceptance(dataDir); err != nil {
			return nil, &AcceptanceError{Op: AcceptanceRecord{}, Dir: dataDir.Name(), Err: err}
		}
		return Accepted{Digest: Digest}, nil
	case accept:
		return Accepted{Digest: Digest}, nil
	case dataDir == nil:
		return AwaitingLicence{}, nil
	}
	switch _, err := dataDir.Stat(acceptanceFile()); {
	case err == nil:
		return Accepted{Digest: Digest}, nil
	case errors.Is(err, fs.ErrNotExist):
		return AwaitingLicence{}, nil
	default:
		return nil, &AcceptanceError{Op: AcceptanceRead{}, Dir: dataDir.Name(), Err: err}
	}
}

// recordAcceptance writes the acceptance record of the pinned digest.
func recordAcceptance(dataDir *os.Root) error {
	if err := dataDir.MkdirAll(acceptanceDir, 0o750); err != nil {
		return err
	}
	return dataDir.WriteFile(acceptanceFile(), []byte(Reference()+"\n"), 0o600)
}

// AcceptanceOp is what LoadLicence was doing with the acceptance record.
// Each operation words its own failure, so every AcceptanceError explains
// itself.
//
//sumtype:decl
type AcceptanceOp interface {
	// explain is the message of a failure err of this operation in data
	// directory dir.
	explain(dir string, err error) string
}

// AcceptanceRecord is saving an acceptance given now with AcceptFlag or
// AcceptEnv.
type AcceptanceRecord struct{}

// AcceptanceRead is reading an acceptance an earlier run recorded.
type AcceptanceRead struct{}

func (AcceptanceRecord) explain(dir string, err error) string {
	return fmt.Sprintf("ddblocal: the DynamoDB Local licence was accepted, but the acceptance could not be saved to %s: %v; "+
		"make %s writable, or start without a data directory and accept on every run; nothing has been pulled",
		acceptancePath(dir), err, filepath.Join(dir, filepath.FromSlash(acceptanceDir)))
}

func (AcceptanceRead) explain(dir string, err error) string {
	return fmt.Sprintf("ddblocal: cannot read the DynamoDB Local licence acceptance at %s: %v; "+
		"make it readable, or pass %s to accept again; nothing has been pulled", acceptancePath(dir), err, AcceptFlag)
}

// acceptancePath is the host path of the acceptance record under dir.
func acceptancePath(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(acceptanceFile()))
}

// AcceptanceError is a licence acceptance that could not be recorded in, or
// read from, the data directory Dir. Nothing has been pulled.
type AcceptanceError struct {
	Op  AcceptanceOp
	Dir string
	Err error
}

func (e *AcceptanceError) Error() string { return e.Op.explain(e.Dir, e.Err) }

func (e *AcceptanceError) Unwrap() error { return e.Err }

// Notice is the text shown before the first pull and in every 501 a DynamoDB
// call answers while the licence is not accepted (dynamodb-local-terms.md
// 5.1). understudy never prompts, so it ends with how to accept.
func Notice() string {
	return `understudy runs the DynamoDB service on Amazon DynamoDB Local. understudy does not
include or redistribute it. It will be pulled onto this machine from a public registry:

  image    ` + Reference() + `
  licence  Amazon DynamoDB Local License Agreement (AWS, not Apache-2.0).
           After the pull it is at /home/dynamodblocal/LICENSE.txt inside the image.

That licence says, among other terms, that you may use the software "solely (i) for your
internal business purposes and (ii) in connection with the Services", and that "You may
not use the Software if you do not have an AWS account in good standing." AWS can change
or end those terms without notice.

If that does not describe you, do not enable the DynamoDB service. Every other
understudy service works without an AWS account.

understudy starts DynamoDB Local with its telemetry turned off. The image is pulled once;
after that DynamoDB Local needs no network access.

To accept, start understudy with ` + AcceptFlag + ` or set ` + AcceptEnv + `=1.`
}

// BannerLine is the startup banner's and understudy env's line for l.
func BannerLine(l Licence) string {
	switch l := l.(type) {
	case Accepted:
		if covers(l) {
			return "DynamoDB: DynamoDB Local licence accepted for " + Reference()
		}
	case Disabled:
		return "DynamoDB: disabled by configuration"
	case AwaitingLicence, nil:
	}
	return "DynamoDB: disabled until you accept the DynamoDB Local licence: start with " + AcceptFlag + " or set " + AcceptEnv + "=1"
}

// LicenceError is a Start refused because l does not cover the pinned
// image; its message is what a DynamoDB call answers.
type LicenceError struct{ Licence Licence }

func (e *LicenceError) Error() string {
	if _, ok := e.Licence.(Disabled); ok {
		return "The DynamoDB service is disabled by the DynamoDB setting of the understudy configuration (understudy.json, or the file --config names); " +
			"remove that setting and restart understudy to use DynamoDB."
	}
	return Notice()
}
