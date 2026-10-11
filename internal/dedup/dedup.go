// Package dedup drops readings that arrive twice (IF-4). MQTT QoS 1 delivers
// at least once, so after a reconnect the broker may send a reading again; a
// reading is identified by its sensor and its seq, which is the model time
// it was taken.
package dedup

import "sync"

// Remember is how many of a sensor's latest readings are remembered. A
// redelivery repeats something recent, so a short memory is enough.
const Remember = 16

// Set remembers the recent readings of every sensor.
//
// It asks "have I seen exactly this reading?", not "is this older than the
// newest I have seen?". The second question is cheaper and was what the
// first version asked, and it is wrong whenever time does not only move
// forward: one reading stamped with a later model time (a sensor that
// started while the building's clock still showed the previous run's last
// minute) made storage discard every later reading of that sensor as a
// duplicate. A model clock that is moved back within a run did the same.
type Set struct {
	mu     sync.Mutex
	recent map[string]*ring
}

type ring struct {
	seqs [Remember]int64
	n    int // how many are filled
	next int // where the next one goes
}

func New() *Set { return &Set{recent: map[string]*ring{}} }

// Fresh reports whether (key, seq) has not been seen among the key's recent
// readings, and remembers it.
func (s *Set) Fresh(key string, seq int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recent[key]
	if !ok {
		r = &ring{}
		s.recent[key] = r
	}
	for i := 0; i < r.n; i++ {
		if r.seqs[i] == seq {
			return false
		}
	}
	r.seqs[r.next] = seq
	r.next = (r.next + 1) % Remember
	r.n = min(r.n+1, Remember)
	return true
}
