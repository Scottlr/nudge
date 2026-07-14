package tui

import (
	"testing"
	"time"
)

func TestRenderSchedulerCoalescesAndStops(t *testing.T) {
	t.Parallel()

	scheduler, err := NewRenderScheduler(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if plan := scheduler.Invalidate(); plan.Command != nil {
		t.Fatal("non-animated invalidation scheduled a tick")
	}
	first := scheduler.StartVisibleWork()
	if first.Command == nil || first.Epoch == 0 {
		t.Fatalf("visible work did not schedule a tick: %#v", first)
	}
	if plan := scheduler.Invalidate(); plan.Command != nil {
		t.Fatal("duplicate invalidation created an overlapping tick")
	}
	accepted, next := scheduler.AcceptTick(RenderTickMsg{Epoch: first.Epoch})
	if !accepted || next.Command == nil || next.Epoch != first.Epoch {
		t.Fatalf("accepted tick did not continue one chain: accepted=%v plan=%#v", accepted, next)
	}
	scheduler.StopVisibleWork()
	accepted, next = scheduler.AcceptTick(RenderTickMsg{Epoch: next.Epoch})
	if accepted || next.Command != nil {
		t.Fatal("stale tick survived visible-work stop")
	}
}

func TestRenderSchedulerReducedMotionHasNoTick(t *testing.T) {
	t.Parallel()

	scheduler := DefaultRenderScheduler()
	if plan := scheduler.StartVisibleWork(); plan.Command == nil {
		t.Fatal("default scheduler did not start visible work")
	}
	if plan := scheduler.SetReducedMotion(true); plan.Command != nil {
		t.Fatal("reduced motion scheduled a tick")
	}
	accepted, _ := scheduler.AcceptTick(RenderTickMsg{Epoch: 1})
	if accepted {
		t.Fatal("reduced-motion stale tick was accepted")
	}
	if plan := scheduler.SetReducedMotion(false); plan.Command == nil {
		t.Fatal("disabling reduced motion did not resume visible work")
	}
}

func TestRootOwnsSchedulerAndAnimationFrame(t *testing.T) {
	t.Parallel()

	model := NewModel(nil)
	updated, command := model.Update(StartVisibleAnimationMsg{})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("root did not schedule visible animation")
	}
	updated, command = model.Update(InvalidateRenderMsg{})
	model = updated.(*Model)
	if command != nil {
		t.Fatal("root created an overlapping tick for a coalesced invalidation")
	}
	updated, command = model.Update(RenderTickMsg{Epoch: model.scheduler.epoch})
	model = updated.(*Model)
	if model.AnimationFrame() != 1 || command == nil {
		t.Fatalf("root did not advance and reschedule its frame: frame=%d command=%v", model.AnimationFrame(), command != nil)
	}
}
