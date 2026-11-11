package kernel

import "sync"

// orders hands out delivery order numbers (Delivery.Order) and tracks the
// ones a committed write drew but has not yet handed to the DeliverSink.
// A write draws its numbers under the source cell's lock, so two writes to
// one cell number their deliveries in commit order; it hands them over
// after unlock, so a later-numbered delivery can reach the sink first.
// HandedOver lets the sink wait for the earlier ones.
type orders struct {
	mu   sync.Mutex
	last uint64
	// pending holds the drawn numbers not yet handed over.
	pending map[uint64]struct{}
	// changed is closed and replaced when a pending number is handed over.
	changed chan struct{}
}

func newOrders() *orders {
	return &orders{pending: map[uint64]struct{}{}, changed: make(chan struct{})}
}

// draw returns n numbers in increasing order, pending until done.
func (o *orders) draw(n int) []uint64 {
	if n == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]uint64, n)
	for i := range out {
		o.last++
		out[i] = o.last
		o.pending[o.last] = struct{}{}
	}
	return out
}

// done marks n handed over, whether the sink took or refused it.
func (o *orders) done(n uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.pending[n]; !ok {
		return
	}
	delete(o.pending, n)
	close(o.changed)
	o.changed = make(chan struct{})
}

// handedOver reports whether every number up to n has been handed over;
// when not, changed is closed by the next hand-over.
func (o *orders) handedOver(n uint64) (ok bool, changed <-chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for p := range o.pending {
		if p <= n {
			return false, o.changed
		}
	}
	return true, nil
}

// next returns a number for a delivery handed over at once.
func (o *orders) next() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.last++
	return o.last
}

// raise makes every later number greater than past.
func (o *orders) raise(past uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.last = max(o.last, past)
}

// NextOrder returns a delivery order number for a delivery its caller
// hands over at once, outside a Write: it orders after every number drawn
// before it.
func (k *Kernel) NextOrder() uint64 { return k.orders.next() }

// RaiseOrder makes every delivery order number drawn from now on greater
// than past. A snapshot load raises it past the loaded deliveries' numbers.
func (k *Kernel) RaiseOrder(past uint64) { k.orders.raise(past) }

// HandedOver reports whether every delivery numbered up to order has been
// handed to the DeliverSink. When one has not, changed is closed once the
// next delivery is handed over, so the caller can ask again. A FIFO lane
// waits on it so an earlier commit's delivery is not overtaken while it is
// between its write's unlock and the sink.
func (k *Kernel) HandedOver(order uint64) (ok bool, changed <-chan struct{}) {
	return k.orders.handedOver(order)
}
