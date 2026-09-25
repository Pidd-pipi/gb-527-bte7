package scheduler

import (
	"testing"
	"time"

	"satellite-contact-window-deconfliction/backend/internal/constants"
	"satellite-contact-window-deconfliction/backend/internal/model"
)

func timeShiftFixture() (ConflictGroup, *CandidateGenerator) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	station := model.GroundStation{ID: 1, StationCode: "GS", AntennaCount: 1, SupportedBandsJSON: `["S"]`, SlewBufferSec: 60, StationStatus: "active"}
	assets := []model.SatelliteAsset{
		{ID: 1, SatelliteCode: "SAT-1", SupportedBandsJSON: `["S"]`},
		{ID: 2, SatelliteCode: "SAT-2", SupportedBandsJSON: `["S"]`},
	}
	windows := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(10 * time.Minute), Band: "S", Priority: 5, Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 2, StartAt: base.Add(5 * time.Minute), EndAt: base.Add(15 * time.Minute), Band: "S", Priority: 3, Version: 1},
	}
	group := newGroup(constants.ConflictTypeStationCapacity, windows, 1, 2, 0, "capacity", nil)
	generator := NewCandidateGenerator(Weights{PriorityLoss: 4, MovementDistance: .02, ContactDuration: .003, ResourceMargin: 2}, []model.GroundStation{station}, assets, windows)
	return group, generator
}

func findShift(suggestions []Suggestion) *Suggestion {
	for index := range suggestions {
		if suggestions[index].ActionType == "shift_window_time" {
			return &suggestions[index]
		}
	}
	return nil
}

func TestTimeShiftFindsSmallestBufferedSlot(t *testing.T) {
	group, generator := timeShiftFixture()
	first, second := generator.Generate(group), generator.Generate(group)
	shift := findShift(first)
	if shift == nil {
		t.Fatal("expected a time shift suggestion")
	}
	// Window #2 starts at 10:05; the 60 second slew buffer on both sides requires
	// a 120 second gap, so the earliest clear start is 10:12, a +7 minute shift.
	if shift.ShiftMinutes != 7 {
		t.Fatalf("expected +7 minute shift, got %+d", shift.ShiftMinutes)
	}
	if len(shift.MoveWindowIDs) != 1 || shift.MoveWindowIDs[0] != 2 {
		t.Fatalf("expected only window 2 to move, got %v", shift.MoveWindowIDs)
	}
	if len(shift.KeepWindowIDs) != 1 || shift.KeepWindowIDs[0] != 1 {
		t.Fatalf("expected window 1 to stay, got %v", shift.KeepWindowIDs)
	}
	wantStart := time.Date(2026, 9, 25, 10, 12, 0, 0, time.UTC)
	if shift.ShiftedStartAt == nil || !shift.ShiftedStartAt.Equal(wantStart) {
		t.Fatalf("expected shifted start %s, got %v", wantStart, shift.ShiftedStartAt)
	}
	if shift.ShiftedEndAt == nil || !shift.ShiftedEndAt.Equal(wantStart.Add(10*time.Minute)) {
		t.Fatalf("shifted end must preserve the 10 minute duration, got %v", shift.ShiftedEndAt)
	}
	if shift.OriginalStartAt == nil || !shift.OriginalStartAt.Equal(time.Date(2026, 9, 25, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("original start must be recorded, got %v", shift.OriginalStartAt)
	}
	if shift.Score.ContactDurationSec != 600 || shift.Score.PriorityLoss != 0 || shift.Score.MovementDistanceKM != 0 {
		t.Fatalf("unexpected score breakdown %+v", shift.Score)
	}
	again := findShift(second)
	if again == nil || again.ActionKey != shift.ActionKey {
		t.Fatal("same input must produce the same time shift suggestion")
	}
	for index := range first {
		if first[index].ActionKey != second[index].ActionKey {
			t.Fatal("suggestion ordering is not stable")
		}
	}
}

func TestTimeShiftNeverMovesLockedWindows(t *testing.T) {
	group, generator := timeShiftFixture()
	for index := range group.Windows {
		group.Windows[index].Locked = true
	}
	generator.allWindows = group.Windows
	suggestions := generator.Generate(group)
	if findShift(suggestions) != nil {
		t.Fatal("locked windows must not be suggested for a time shift")
	}
	manual := false
	for _, suggestion := range suggestions {
		manual = manual || suggestion.ActionType == "manual_review"
	}
	if !manual {
		t.Fatal("manual review must remain when no slot can be offered")
	}
}

func TestTimeShiftFallsBackToManualWhenNoSlotExists(t *testing.T) {
	group, generator := timeShiftFixture()
	blocker := model.ContactWindow{ID: 9, StationID: 1, SatelliteID: 2, StartAt: group.Windows[0].StartAt.Add(-30 * time.Minute), EndAt: group.Windows[0].StartAt.Add(60 * time.Minute), Band: "S", Version: 1}
	generator.allWindows = append(append([]model.ContactWindow(nil), group.Windows...), blocker)
	suggestions := generator.Generate(group)
	if findShift(suggestions) != nil {
		t.Fatal("no free slot exists inside ±15 minutes; shift suggestion must be omitted")
	}
	manual := false
	for _, suggestion := range suggestions {
		manual = manual || suggestion.ActionType == "manual_review"
	}
	if !manual {
		t.Fatal("manual review fallback expected when no slot exists")
	}
}

func TestTimeShiftSkippedForUnshiftableConflictTypes(t *testing.T) {
	group, generator := timeShiftFixture()
	for _, conflictType := range []string{constants.ConflictTypeBandMismatch, constants.ConflictTypeDurationShortfall} {
		group.ConflictType = conflictType
		if findShift(generator.Generate(group)) != nil {
			t.Fatalf("time shift cannot repair %s", conflictType)
		}
	}
}
