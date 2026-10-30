package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// activityTitle names the activity pane on its spine and in the dock's tooltip.
const activityTitle = "Recent activity"

// activityPane builds the recent-activity pane: the floor's pulse beside any
// pane, its stream live while the pane holds focus. A room without a change
// feed has no activity to list and says so.
func (h spatialPanes) activityPane(ctx context.Context, t trail, i int) (paneVM, error) {
	vm := paneVM{
		Mode:     paneMode(t, i),
		Kind:     paneActivity,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    activityTitle,
		World:    "activity",
	}
	if vm.Mode == "spine" {
		return vm, nil
	}
	if h.pulse == nil {
		vm.Activity = &pulseVM{Unavailable: true}
		return vm, nil
	}
	activity, err := h.pulseSection(ctx, t, i)
	if err != nil {
		return paneVM{}, err
	}
	vm.Activity = activity
	return vm, nil
}

// pulseSection lists the reader's recent activity for pane i, streaming when
// the pane holds focus. A refused list fails the pane; any other failure shows
// as unavailable until the stream, if any, lists again.
func (h spatialPanes) pulseSection(ctx context.Context, t trail, i int) (*pulseVM, error) {
	pulses, err := h.pulse.Pulse(ctx)
	if errors.Is(err, domain.ErrUnauthorized) {
		return nil, fmt.Errorf("list recent activity: %w", err)
	}
	section := &pulseVM{Items: pulseItems(pulses, t, i), Unavailable: err != nil}
	if i == t.Focus {
		section.StreamURL = livePulseURL(t, i)
	}
	return section, nil
}
