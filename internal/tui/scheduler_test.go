package tui

import (
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
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
	updated, _ := model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(*Model)
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

func TestRenderSchedulerVisibleWorkCountAndSyncedStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scheduler := DefaultRenderScheduler()
		first := scheduler.SetVisibleAnimatedWork(2)
		if first.Command == nil || scheduler.VisibleAnimatedWork() != 2 {
			t.Fatalf("visible work = %d, plan = %#v", scheduler.VisibleAnimatedWork(), first)
		}
		if next := scheduler.SetVisibleAnimatedWork(3); next.Command != nil {
			t.Fatal("changing a nonzero count created an overlapping tick")
		}

		messages := make(chan RenderTickMsg, 1)
		go func() {
			time.Sleep(defaultAnimationInterval)
			messages <- RenderTickMsg{Epoch: first.Epoch}
		}()
		scheduler.SetVisibleAnimatedWork(0)
		time.Sleep(defaultAnimationInterval)
		synctest.Wait()
		select {
		case message := <-messages:
			if accepted, _ := scheduler.AcceptTick(message); accepted {
				t.Fatal("stopped scheduler accepted a queued tick")
			}
		default:
			t.Fatal("synced tick command did not complete")
		}
	})
}
