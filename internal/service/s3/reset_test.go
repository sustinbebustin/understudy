package s3

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TestResetWithAFailingIndexImportKeepsTheState: when Reset cannot write
// the kept index entries back, the index and every bucket stay as they
// were, rather than the index being left empty.
func TestResetWithAFailingIndexImportKeepsTheState(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		s := New(k.Deps(s3api.Service()), Options{})
		failing := false
		codec := stateCodec()
		s.index = store.NewPartitioned(k.Kernel.Gate(), store.Codec{
			Marshal: codec.Marshal,
			Unmarshal: func(b []byte, v any) error {
				if failing {
					return errors.New("injected index decode failure")
				}
				return codec.Unmarshal(b, v)
			},
		}, newIndex)
		f := commitFixture{k: k, s: s}
		for _, name := range []string{"kept-a", "kept-b"} {
			_, err := kerneltest.Dispatch[s3api.CreateBucketInput, s3api.CreateBucketOutput](k.Context(), k, f.call(s3api.OpCreateBucket), &s3api.CreateBucketInput{Bucket: name})
			f.must(err)
		}

		other, aerr := aws.ParseAccountID("999999999999")
		if aerr != nil {
			k.Fatal(aerr)
		}
		failing = true
		s.Reset(kernel.ResetScope{Accounts: []aws.AccountID{other}})

		if got := slices.Sorted(maps.Keys(s.homes())); !slices.Equal(got, []string{"kept-a", "kept-b"}) {
			k.Errorf("index after the failed reset = %v, want both buckets", got)
		}
		for _, name := range []string{"kept-a", "kept-b"} {
			if _, ok := s.buckets.Lookup(name); !ok {
				k.Errorf("bucket cell %s is gone after the failed reset", name)
			}
		}
	}, kerneltest.Services(s3api.Service()))
}
