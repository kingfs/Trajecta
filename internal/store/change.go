package store

/*
 * Coarse change notifications.
 *
 * The console is told that an area of the data changed, never the change itself.
 * That is deliberate: a single recorded request moves the trace list, the
 * session list, the overview aggregates and the analysis queue at once, so a
 * finer unit than "traffic changed" would only make the browser refetch one
 * page in several pieces. The subscriber (the Monitor realtime socket) fans the
 * topic out to every connected browser, and each browser refetches whatever it
 * has on screen for that topic.
 *
 * A topic is a plain string so a subscriber can log or filter on it without
 * sharing an enum. A notification that cannot be delivered is dropped rather
 * than queued: the signal is idempotent, and the next write produces the next
 * one.
 */
const (
	// ChangeTraffic covers every read a recorded request can move: the trace and
	// session lists, the overview aggregates, findings, and the analysis queue.
	ChangeTraffic = "traffic"
	// ChangeEvents covers the runtime event feed.
	ChangeEvents = "events"
)

// SubscribeChanges returns the topics published from now on, plus the function
// that stops the subscription. The unsubscribe function closes the channel, so
// a reader can range over it and a blocked reader wakes up.
func (s *Store) SubscribeChanges(buffer int) (<-chan string, func()) {
	if buffer <= 0 {
		buffer = 8
	}
	ch := make(chan string, buffer)
	s.shared.changeMu.Lock()
	if s.shared.changeSubs == nil {
		s.shared.changeSubs = map[chan string]struct{}{}
	}
	s.shared.changeSubs[ch] = struct{}{}
	s.shared.changeMu.Unlock()
	return ch, func() {
		s.shared.changeMu.Lock()
		if _, ok := s.shared.changeSubs[ch]; ok {
			delete(s.shared.changeSubs, ch)
			close(ch)
		}
		s.shared.changeMu.Unlock()
	}
}

// notifyChange publishes one topic. Every send is non-blocking, and the sends
// happen under the same mutex the unsubscribe closure takes to close a channel -
// the property that makes a send on a closed channel impossible. See the comment
// on notifySystemEventChanged for why the mutex is held across the loop instead
// of publishing from a snapshot.
func (s *Store) notifyChange(topic string) {
	if s == nil || s.shared == nil {
		return
	}
	s.shared.changeMu.Lock()
	for ch := range s.shared.changeSubs {
		select {
		case ch <- topic:
		default:
		}
	}
	s.shared.changeMu.Unlock()
}
