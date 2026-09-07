package tmux

import "testing"

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
