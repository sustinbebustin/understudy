package secretsmanager

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// The staging labels Secrets Manager itself moves.
const (
	stageCurrent  = "AWSCURRENT"
	stagePrevious = "AWSPREVIOUS"
	stagePending  = "AWSPENDING"
)

// Quotas of one secret (secrets-logs-events-alarms.md 2.2 and 2.6).
const (
	maxLabels   = 20
	maxVersions = 100
	// deprecatedAge is how old a version without labels must be before it
	// may be removed to stay within maxVersions.
	deprecatedAge = 24 * time.Hour
)

// attach moves label onto version id. Moving AWSCURRENT away from a version
// moves AWSPREVIOUS onto that version, as every AWSCURRENT move does in AWS.
func (s *Secret) attach(label, id string) {
	from, held := s.Stages[label]
	s.Stages[label] = id
	if label == stageCurrent && held && from != id {
		s.Stages[stagePrevious] = from
	}
}

// promote gives version id AWSCURRENT, as PutSecretValue without
// VersionStages and UpdateSecret with a value do. The demoted version also
// loses AWSPENDING: the one AWS-recorded trace of a default put over a
// version holding AWSCURRENT and AWSPENDING leaves it with AWSPREVIOUS only
// (secrets-logs-events-alarms.md 2.2).
func (s *Secret) promote(id string) {
	from, held := s.Stages[stageCurrent]
	s.attach(stageCurrent, id)
	if held && from != id && s.Stages[stagePending] == from {
		delete(s.Stages, stagePending)
	}
}

// addVersion stores v and labels it: with labels when the caller named
// them, else with AWSCURRENT through promote. The first version of a secret
// takes AWSCURRENT whatever labels were named, as PutSecretValue's API
// reference says (secrets-logs-events-alarms.md 2.2). It works on s's own
// maps, so s must have called ownMaps; on an error the caller drops s.
func (s *Secret) addVersion(v Version, labels []string, now clock.Instant) *awserr.Error {
	s.Versions[v.ID] = v
	if labels == nil {
		s.promote(v.ID)
	} else {
		if _, held := s.Stages[stageCurrent]; !held {
			s.attach(stageCurrent, v.ID)
		}
		for _, label := range labels {
			s.attach(label, v.ID)
		}
	}
	if len(s.Stages) > maxLabels {
		return errTooManyLabels()
	}
	return s.prune(now)
}

// prune removes the oldest versions without labels, once they are a day
// old, while the secret holds more than maxVersions; AWS removes deprecated
// versions only then (secrets-logs-events-alarms.md 2.2).
func (s *Secret) prune(now clock.Instant) *awserr.Error {
	if len(s.Versions) <= maxVersions {
		return nil
	}
	held := map[string]bool{}
	for _, id := range s.Stages {
		held[id] = true
	}
	for _, id := range s.versionsByAge() {
		if len(s.Versions) <= maxVersions {
			break
		}
		if !held[id] && !now.Before(s.Versions[id].Created.Add(deprecatedAge)) {
			delete(s.Versions, id)
		}
	}
	if len(s.Versions) > maxVersions {
		return errTooManyVersions()
	}
	return nil
}

// stageMove is what UpdateSecretVersionStage asks for one label.
//
//sumtype:decl
type stageMove interface{ isStageMove() }

// moveLabel attaches the label to To. From must name the version holding
// the label now, when another version holds it.
type moveLabel struct {
	To   string
	From *string
}

// removeLabel takes the label off From.
type removeLabel struct{ From string }

func (moveLabel) isStageMove()   {}
func (removeLabel) isStageMove() {}

// moveStage applies m to label. It works on s's own maps, so s must have
// called ownMaps; on an error the caller drops s.
func (s *Secret) moveStage(label string, m stageMove) *awserr.Error {
	holder, held := s.Stages[label]
	switch m := m.(type) {
	case moveLabel:
		if _, ok := s.Versions[m.To]; !ok {
			return errVersionValueNotFound(m.To)
		}
		if held && holder != m.To {
			switch {
			case m.From == nil:
				return errLabelAttached(label, holder)
			case *m.From != holder:
				return errLabelOnOther(label, holder, *m.From)
			}
		}
		s.attach(label, m.To)
	case removeLabel:
		switch {
		case label == stageCurrent:
			return errCurrentNotRemovable()
		case !held || holder != m.From:
			return errLabelNotOn(label, m.From)
		}
		delete(s.Stages, label)
	}
	if len(s.Stages) > maxLabels {
		return errTooManyLabels()
	}
	return nil
}

// versionsByAge returns the ids of the secret's versions, oldest first, as
// ListSecretVersionIds lists them.
func (s *Secret) versionsByAge() []string {
	return slices.SortedFunc(maps.Keys(s.Versions), func(a, b string) int {
		if c := s.Versions[a].Created.Compare(s.Versions[b].Created); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
}
