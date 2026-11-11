package sns_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/paging/pagingtest"
)

// pageSize is the fixed size of an SNS listing page (sns.md 2.2, 3.4).
const pageSize = 100

// seedSpanningPages is enough items to span three pages of pageSize.
const seedSpanningPages = 2*pageSize + 30

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// firstN is the first n of ids. SNS has no page-size input, so the check's
// one-item peek at where a token resumes reads the head of a full page.
func firstN(ids []string, n int32) []string {
	return ids[:min(len(ids), int(n))]
}

// pagedListing is one SNS listing, driven as a client drives it.
type pagedListing struct {
	op string
	of func(h *harness) pagingtest.Listing
}

// pagedListings are the three SNS listings. The subscription listings
// report each subscription under its ARN, which is what Unsubscribe takes.
func pagedListings() []pagedListing {
	subscriptions := func(h *harness, topic string, page func(marker *string) ([]snsapi.Subscription, *string)) pagingtest.Listing {
		return pagingtest.Listing{
			Add: func(name string) string {
				return h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:"+name, nil)
			},
			Remove: func(arn string) {
				must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})
			},
			Page: func(maxItems int32, marker *string) ([]string, *string) {
				listed, next := page(marker)
				arns := make([]string, len(listed))
				for i, s := range listed {
					arns[i] = deref(s.SubscriptionArn)
				}
				return firstN(arns, maxItems), next
			},
		}
	}
	return []pagedListing{
		{"ListTopics", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string { return h.topic(name, nil) },
				Remove: func(arn string) {
					must(h, snsapi.OpDeleteTopic, &snsapi.DeleteTopicInput{TopicArn: arn})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, snsapi.OpListTopics, &snsapi.ListTopicsInput{NextToken: marker})
					arns := make([]string, len(out.Topics))
					for i, t := range out.Topics {
						arns[i] = deref(t.TopicArn)
					}
					return firstN(arns, maxItems), out.NextToken
				},
			}
		}},
		{"ListSubscriptions", func(h *harness) pagingtest.Listing {
			topic := h.topic("events", nil)
			return subscriptions(h, topic, func(marker *string) ([]snsapi.Subscription, *string) {
				out := must(h, snsapi.OpListSubscriptions, &snsapi.ListSubscriptionsInput{NextToken: marker})
				return out.Subscriptions, out.NextToken
			})
		}},
		{"ListSubscriptionsByTopic", func(h *harness) pagingtest.Listing {
			topic := h.topic("events", nil)
			return subscriptions(h, topic, func(marker *string) ([]snsapi.Subscription, *string) {
				out := must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic, NextToken: marker})
				return out.Subscriptions, out.NextToken
			})
		}},
	}
}

// Every SNS listing, paged while the item each token names, an item
// already served and a new item come and go between pages, serves each item
// once and skips none present throughout. The inferred no-skip rule has no
// recording; see internal/paging.
func TestListingsSurviveChangesBetweenPages(t *testing.T) {
	for _, l := range pagedListings() {
		t.Run(l.op, func(t *testing.T) {
			run(t, func(h *harness) {
				pagingtest.SurvivesChanges(h, l.of(h), seedSpanningPages, pageSize)
			})
		})
	}
}

// A NextToken another listing issued, or one understudy never issued, is
// InvalidParameter naming NextToken (inferred text).
func TestListingsRefuseForeignTokens(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		for i := range pageSize + 1 {
			h.topic(string(rune('a'+i%26))+string(rune('a'+i/26))+"-t", nil)
		}
		foreign := must(h, snsapi.OpListTopics, &snsapi.ListTopicsInput{}).NextToken
		if foreign == nil {
			h.Fatal("ListTopics of 100+ topics gave no NextToken")
		}
		for _, token := range []string{"bogus", *foreign} {
			_, err := call(h, snsapi.OpListSubscriptions, &snsapi.ListSubscriptionsInput{NextToken: &token})
			wantError(h, err, "InvalidParameter", "Invalid parameter: NextToken")
			_, err = call(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic, NextToken: &token})
			wantError(h, err, "InvalidParameter", "Invalid parameter: NextToken")
		}
		_, err := call(h, snsapi.OpListTopics, &snsapi.ListTopicsInput{NextToken: new("bogus")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: NextToken")
	})
}
