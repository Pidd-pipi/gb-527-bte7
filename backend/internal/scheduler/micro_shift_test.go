package scheduler

import (
	"testing"
	"time"

	"satellite-contact-window-deconfliction/backend/internal/constants"
	"satellite-contact-window-deconfliction/backend/internal/model"
)

func microWeights() Weights {
	return Weights{PriorityLoss: 4, MovementDistance: .02, ContactDuration: .003, ResourceMargin: 2}
}

func TestMicroShiftFindsEarliestSlotAndKeepsDurationBandSatellite(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	station := model.GroundStation{ID: 1, StationCode: "GS-1", AntennaCount: 1, SupportedBandsJSON: `["S"]`, SlewBufferSec: 0, StationStatus: "active"}
	asset := model.SatelliteAsset{ID: 7, SatelliteCode: "SAT-7", SupportedBandsJSON: `["S"]`}
	blocker := model.SatelliteAsset{ID: 8, SatelliteCode: "SAT-8", SupportedBandsJSON: `["S"]`}
	windows := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 7, StartAt: base, EndAt: base.Add(10 * time.Minute), Band: "S", Priority: 9, Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 8, StartAt: base, EndAt: base.Add(8 * time.Minute), Band: "S", Priority: 4, Version: 1},
		// Blocks every earlier slot for window 2 within the fifteen minute window.
		{ID: 3, StationID: 1, SatelliteID: 7, StartAt: base.Add(-10 * time.Minute), EndAt: base.Add(-2 * time.Minute), Band: "S", Version: 1},
	}
	group := newGroup(constants.ConflictTypeStationCapacity, []model.ContactWindow{windows[0], windows[1]}, 1, 2, 0, "capacity", nil)
	generator := NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{asset, blocker}, windows)
	suggestion, ok := generator.microShiftWindow(group)
	if !ok {
		t.Fatal("expected a micro-stagger suggestion")
	}
	if suggestion.ActionType != MicroShiftActionType || suggestion.ShiftWindowID == nil || *suggestion.ShiftWindowID != 2 {
		t.Fatalf("unexpected suggestion %+v", suggestion)
	}
	if suggestion.ShiftMinutes != 10 {
		t.Fatalf("expected +10 minute shift, got %d", suggestion.ShiftMinutes)
	}
	if !suggestion.ShiftedStart.Equal(base.Add(10*time.Minute)) || !suggestion.ShiftedEnd.Equal(base.Add(18*time.Minute)) {
		t.Fatalf("unexpected shifted interval %s - %s", suggestion.ShiftedStart, suggestion.ShiftedEnd)
	}
	if suggestion.ShiftedEnd.Sub(suggestion.ShiftedStart) != windows[1].EndAt.Sub(windows[1].StartAt) {
		t.Fatal("contact duration must stay unchanged")
	}
	if len(suggestion.MoveWindowIDs) != 1 || suggestion.MoveWindowIDs[0] != 2 {
		t.Fatalf("only window 2 may move, got %v", suggestion.MoveWindowIDs)
	}
	for _, kept := range suggestion.KeepWindowIDs {
		if kept == 2 {
			t.Fatal("moved window must not be listed as kept")
		}
	}
	if suggestion.Score.MovementDistanceKM != 0 || suggestion.Score.ContactDurationSec != 480 {
		t.Fatalf("score must use zero station distance and unchanged duration, got %+v", suggestion.Score)
	}
}

func TestMicroShiftHonoursSlewBufferConflict(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	station := model.GroundStation{ID: 1, StationCode: "GS-1", AntennaCount: 1, SupportedBandsJSON: `["S"]`, SlewBufferSec: 60, StationStatus: "active"}
	first := model.SatelliteAsset{ID: 1, SatelliteCode: "SAT-1", SupportedBandsJSON: `["S"]`}
	second := model.SatelliteAsset{ID: 2, SatelliteCode: "SAT-2", SupportedBandsJSON: `["S"]`}
	windows := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(10 * time.Minute), Band: "S", Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 2, StartAt: base.Add(11 * time.Minute), EndAt: base.Add(18 * time.Minute), Band: "S", Version: 1},
	}
	group := newGroup(constants.ConflictTypeSlewBuffer, windows, 1, 2, 60, "buffer", nil)
	generator := NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{first, second}, windows)
	suggestion, ok := generator.microShiftWindow(group)
	if !ok {
		t.Fatal("expected micro-stagger for slew buffer conflict")
	}
	if suggestion.ShiftMinutes != 1 {
		t.Fatalf("expected +1 minute shift to clear the 60 second buffer, got %d", suggestion.ShiftMinutes)
	}
}

func TestMicroShiftSkipsLockedWindowsAndNoSlotKeepsManualOnly(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	station := model.GroundStation{ID: 1, StationCode: "GS-1", AntennaCount: 1, SupportedBandsJSON: `["S"]`, SlewBufferSec: 0, StationStatus: "active"}
	asset := model.SatelliteAsset{ID: 1, SatelliteCode: "SAT-1", SupportedBandsJSON: `["S"]`}
	lockedWindows := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(10 * time.Minute), Band: "S", Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 1, StartAt: base.Add(2 * time.Minute), EndAt: base.Add(9 * time.Minute), Band: "S", Locked: true, Version: 1},
	}
	lockedGroup := newGroup(constants.ConflictTypeStationCapacity, lockedWindows, 1, 2, 0, "capacity", nil)
	generator := NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{asset}, lockedWindows)
	if _, ok := generator.microShiftWindow(lockedGroup); ok {
		t.Fatal("locked windows must never participate in micro-stagger")
	}
	blocked := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(40 * time.Minute), Band: "S", Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 1, StartAt: base.Add(5 * time.Minute), EndAt: base.Add(10 * time.Minute), Band: "S", Version: 1},
		// Earlier slots back to -15 minutes stay occupied.
		{ID: 3, StationID: 1, SatelliteID: 1, StartAt: base.Add(-20 * time.Minute), EndAt: base.Add(1 * time.Minute), Band: "S", Version: 1},
	}
	blockedGroup := newGroup(constants.ConflictTypeStationCapacity, blocked[:2], 1, 2, 0, "capacity", nil)
	generator = NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{asset}, blocked)
	if _, ok := generator.microShiftWindow(blockedGroup); ok {
		t.Fatal("expected no suggestion when no slot exists within fifteen minutes")
	}
	suggestions := generator.Generate(blockedGroup)
	for _, suggestion := range suggestions {
		if suggestion.ActionType == MicroShiftActionType {
			t.Fatal("micro-stagger must be absent, leaving manual handling")
		}
	}
}

func TestMicroShiftIgnoredForBandAndDurationConflictsAndStaysStable(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	station := model.GroundStation{ID: 1, StationCode: "GS-1", AntennaCount: 2, SupportedBandsJSON: `["X"]`, SlewBufferSec: 0, StationStatus: "active"}
	asset := model.SatelliteAsset{ID: 1, SatelliteCode: "SAT-1", SupportedBandsJSON: `["X"]`, MinimumContactSec: 600}
	window := model.ContactWindow{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(5 * time.Minute), Band: "S", Version: 1}
	generator := NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{asset}, []model.ContactWindow{window})
	for _, conflictType := range []string{constants.ConflictTypeBandMismatch, constants.ConflictTypeDurationShortfall, constants.ConflictTypeSatelliteOverlap} {
		group := newGroup(conflictType, []model.ContactWindow{window}, 0, 0, 0, conflictType, nil)
		if _, ok := generator.microShiftWindow(group); ok {
			t.Fatalf("%s conflicts must not offer micro-stagger", conflictType)
		}
	}
	station.SupportedBandsJSON = `["S"]`
	capacityWindows := []model.ContactWindow{
		{ID: 1, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(10 * time.Minute), Band: "S", Locked: true, Version: 1},
		{ID: 2, StationID: 1, SatelliteID: 1, StartAt: base, EndAt: base.Add(8 * time.Minute), Band: "S", Version: 1},
	}
	group := newGroup(constants.ConflictTypeStationCapacity, capacityWindows, 1, 2, 0, "capacity", nil)
	generator = NewCandidateGenerator(microWeights(), []model.GroundStation{station}, []model.SatelliteAsset{asset}, capacityWindows)
	first, second := generator.Generate(group), generator.Generate(group)
	var firstKey, secondKey string
	for _, suggestion := range first {
		if suggestion.ActionType == MicroShiftActionType {
			firstKey = suggestion.ActionKey
		}
	}
	for _, suggestion := range second {
		if suggestion.ActionType == MicroShiftActionType {
			secondKey = suggestion.ActionKey
		}
	}
	if firstKey == "" || firstKey != secondKey {
		t.Fatalf("micro-stagger ranking must be stable, got %q and %q", firstKey, secondKey)
	}
}
