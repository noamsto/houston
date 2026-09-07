package tmux

import (
	"bufio"
	"strings"
	"testing"
)

func TestSubscribeDeliversOutput(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	cc.dispatch("%1", []byte("hello"))

	select {
	case ev := <-sub.C():
		if ev.Dirty {
			t.Fatalf("got dirty event, want data")
		}
		if string(ev.Data) != "hello" {
			t.Fatalf("got %q, want %q", ev.Data, "hello")
		}
	default:
		t.Fatal("no event delivered")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")
	cc.Unsubscribe("%1", sub)

	cc.dispatch("%1", []byte("hello"))

	select {
	case ev := <-sub.C():
		t.Fatalf("got event %+v after unsubscribe", ev)
	default:
	}
}

func TestUnsubscribeRemovesOnlyThatSubscriber(t *testing.T) {
	cc := NewControlClient("test")
	a := cc.Subscribe("%1")
	b := cc.Subscribe("%1")
	cc.Unsubscribe("%1", a)

	cc.dispatch("%1", []byte("hello"))

	if len(a.C()) != 0 {
		t.Fatal("unsubscribed handle still received output")
	}
	if len(b.C()) != 1 {
		t.Fatalf("remaining subscriber got %d events, want 1", len(b.C()))
	}
}

// fillSub saturates a subscriber's buffer so the next dispatch must drop.
func fillSub(cc *ControlClient, paneID string, s *PaneSub) {
	for i := 0; i < cap(s.ch); i++ {
		cc.dispatch(paneID, []byte("x"))
	}
}

func TestDropMarksDirtyAndDiscardsStaleBuffer(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	fillSub(cc, "%1", sub)
	cc.dispatch("%1", []byte("this one cannot fit"))

	ev := <-sub.C()
	if !ev.Dirty {
		t.Fatalf("first event after a drop = %+v, want Dirty", ev)
	}
	if len(sub.C()) != 0 {
		t.Fatalf("%d stale events survived the drop, want 0", len(sub.C()))
	}
}

func TestDirtySuppressesDeliveryUntilAcked(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	fillSub(cc, "%1", sub)
	cc.dispatch("%1", []byte("overflow"))
	<-sub.C() // consume the Dirty marker

	cc.dispatch("%1", []byte("still suppressed"))
	if len(sub.C()) != 0 {
		t.Fatal("delivered output while dirty; subscriber has not re-seeded yet")
	}

	cc.AckReseed(sub)
	cc.dispatch("%1", []byte("after reseed"))

	ev := <-sub.C()
	if string(ev.Data) != "after reseed" {
		t.Fatalf("got %q, want %q", ev.Data, "after reseed")
	}
}

func TestDropOnOneSubscriberDoesNotAffectAnother(t *testing.T) {
	cc := NewControlClient("test")
	slow := cc.Subscribe("%1")
	fast := cc.Subscribe("%1")

	fillSub(cc, "%1", slow) // also fills fast; drain fast so it has room
	for len(fast.C()) > 0 {
		<-fast.C()
	}

	cc.dispatch("%1", []byte("live"))

	if ev := <-slow.C(); !ev.Dirty {
		t.Fatalf("slow subscriber = %+v, want Dirty", ev)
	}
	if ev := <-fast.C(); ev.Dirty {
		t.Fatal("fast subscriber was marked dirty by its neighbour's drop")
	}
}

// feed runs the read loop over a canned control-mode transcript.
func feed(cc *ControlClient, transcript string) {
	cc.readLoop(bufio.NewReader(strings.NewReader(transcript)))
}

func TestPauseMarksDirty(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%output %1 before\\015\\012\n%pause %1\n")

	ev := <-sub.C()
	if !ev.Dirty {
		t.Fatalf("first event after %%pause = %+v, want Dirty", ev)
	}
	if len(sub.C()) != 0 {
		t.Fatalf("%d events survived the pause, want 0 — the backlog is discarded", len(sub.C()))
	}
}

func TestContinueDoesNotMarkDirty(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%continue %1\n")

	if len(sub.C()) != 0 {
		t.Fatal("continue marked the pane dirty; only pause may")
	}
}

func TestPauseOnAnotherPaneIsIgnored(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%pause %2\n")

	if len(sub.C()) != 0 {
		t.Fatal("a pause on %2 marked %1 dirty")
	}
}
