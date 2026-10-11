package dynamodb

import (
	"context"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Continuous backups are a stored setting Local lacks (dynamodb-local.md
// 4.2, 6): every table reports them ENABLED, and point-in-time recovery is
// recorded and described on the emulator clock. No backup is taken.

const (
	// defaultRecoveryDays is RecoveryPeriodInDays when enabling names none,
	// the longest period AWS allows (dynamodb-local.md Implications 7).
	defaultRecoveryDays = 35
	// restoreLag is how far LatestRestorableDateTime trails the current
	// time: "typically 5 minutes" (dynamodb-local.md 6).
	restoreLag = 5 * time.Minute
)

func (s *Service) describeContinuousBackups(ctx context.Context, c *kernel.Call, in *dynamodbapi.DescribeContinuousBackupsInput) (smithy.Serializable, *awserr.Error) {
	name, err := backedUpName(c.Scope(), in.TableName)
	if err != nil {
		return nil, err
	}
	t, err := s.recorded(ctx, c, name, errBackedUpNotFound(name))
	if err != nil {
		return nil, err
	}
	return &dynamodbapi.DescribeContinuousBackupsOutput{ContinuousBackupsDescription: backups(t.PITR, c.Admitted())}, nil
}

// updateContinuousBackups enables or disables point-in-time recovery.
// Enabling an enabled table keeps when it was enabled and, unless the call
// names a new one, its recovery period. Recovery is enabled at c's
// admission instant, the instant the answer describes it at.
func (s *Service) updateContinuousBackups(ctx context.Context, c *kernel.Call, in *dynamodbapi.UpdateContinuousBackupsInput) (smithy.Serializable, *awserr.Error) {
	name, err := backedUpName(c.Scope(), in.TableName)
	if err != nil {
		return nil, err
	}
	missing := errBackedUpNotFound(name)
	if _, err := s.recorded(ctx, c, name, missing); err != nil {
		return nil, err
	}
	spec := in.PointInTimeRecoverySpecification
	t, err := s.alter(c, name, missing, func(t *Table, _ clock.Instant) *awserr.Error {
		if !spec.PointInTimeRecoveryEnabled {
			t.PITR = nil
			return nil
		}
		next := PITR{Since: c.Admitted(), Days: defaultRecoveryDays}
		if t.PITR != nil {
			next = *t.PITR
		}
		if spec.RecoveryPeriodInDays != nil {
			next.Days = *spec.RecoveryPeriodInDays
		}
		t.PITR = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &dynamodbapi.UpdateContinuousBackupsOutput{ContinuousBackupsDescription: backups(t.PITR, c.Admitted())}, nil
}

// backups describes continuous backups at now: ENABLED on every table, as
// AWS enables them at creation, and the recovery window of p, which the
// description always carries because the Terraform provider reads a
// missing one as no answer (dynamodb-local.md 6).
func backups(p *PITR, now clock.Instant) *dynamodbapi.ContinuousBackupsDescription {
	recovery := &dynamodbapi.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: new(dynamodbapi.PointInTimeRecoveryStatusDisabled)}
	if p != nil {
		latest := now.Add(-restoreLag)
		if latest.Before(p.Since) {
			latest = p.Since
		}
		earliest := now.Add(-time.Duration(p.Days) * 24 * time.Hour)
		if earliest.Before(p.Since) {
			earliest = p.Since
		}
		recovery = &dynamodbapi.PointInTimeRecoveryDescription{
			PointInTimeRecoveryStatus:  new(dynamodbapi.PointInTimeRecoveryStatusEnabled),
			RecoveryPeriodInDays:       new(p.Days),
			EarliestRestorableDateTime: new(earliest.Wire()),
			LatestRestorableDateTime:   new(latest.Wire()),
		}
	}
	return &dynamodbapi.ContinuousBackupsDescription{ContinuousBackupsStatus: dynamodbapi.ContinuousBackupsStatusEnabled, PointInTimeRecoveryDescription: recovery}
}

// backedUpName is the table a continuous-backups call names; a table ARN
// of another account or region is a table this scope lacks.
func backedUpName(scope aws.Scope, table string) (string, *awserr.Error) {
	if inScope(scope, []string{table}, answer{}) != nil {
		return "", errBackedUpNotFound(tableName(table))
	}
	return tableName(table), nil
}

// errBackedUpNotFound is the continuous-backups operations' answer for a
// missing table: TableNotFoundException, which the model gives them and the
// Terraform provider tolerates on read (tf-storage.md 3.2). The text is
// UNVERIFIED against AWS.
func errBackedUpNotFound(name string) *awserr.Error {
	return dynamodbapi.ErrTableNotFoundException("Table not found: " + name)
}
