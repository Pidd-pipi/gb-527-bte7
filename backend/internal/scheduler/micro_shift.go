package scheduler

import (
	"fmt"
	"sort"
	"time"

	"satellite-contact-window-deconfliction/backend/internal/constants"
	"satellite-contact-window-deconfliction/backend/internal/model"
)

// MicroShiftActionType marks a same-station, same-band micro-stagger suggestion
// that only moves one window's start time within MicroShiftLimit.
const MicroShiftActionType = "micro_time_shift"

// MicroShiftLimit bounds how far a contact window may be nudged relative to its
// original start time (inclusive on both sides).
const MicroShiftLimit = 15 * time.Minute

// microShiftStep is the deterministic search granularity inside the limit.
const microShiftStep = time.Minute

type microShiftCandidate struct {
	affected model.ContactWindow
	offset   time.Duration
	margin   int
	startAt  time.Time
	endAt    time.Time
}

// microShiftWindow looks for a non-conflicting slot within +/- fifteen minutes
// of the original start time. Satellite, band and duration never change, locked
// windows never participate, and every candidate must clear the station's slew
// buffer against every already loaded window.
func (generator *CandidateGenerator) microShiftWindow(group ConflictGroup) (Suggestion, bool) {
	if group.ConflictType != constants.ConflictTypeStationCapacity && group.ConflictType != constants.ConflictTypeSlewBuffer {
		return Suggestion{}, false
	}
	station, ok := generator.stations[group.Windows[0].StationID]
	if !ok || station.StationStatus != "active" {
		return Suggestion{}, false
	}
	buffer := time.Duration(station.SlewBufferSec) * time.Second
	candidates := make([]microShiftCandidate, 0)
	for _, affected := range group.Windows {
		if affected.Locked || affected.StationID != station.ID {
			continue
		}
		candidate, feasible := generator.nearestMicroSlot(affected, station, buffer)
		if !feasible {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return Suggestion{}, false
	}
	// Prefer slots leaving the most resource headroom, then the smallest move,
	// then the earlier slot, then the smallest window ID. This order is the only
	// ranking path and therefore stays stable for identical inputs.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].margin != candidates[j].margin {
			return candidates[i].margin > candidates[j].margin
		}
		di, dj := absDuration(candidates[i].offset), absDuration(candidates[j].offset)
		if di != dj {
			return di < dj
		}
		if candidates[i].offset != candidates[j].offset {
			return candidates[i].offset < candidates[j].offset
		}
		return candidates[i].affected.ID < candidates[j].affected.ID
	})
	best := candidates[0]
	shiftMinutes := int(best.offset / time.Minute)
	keep := make([]uint, 0, len(group.Windows)-1)
	for _, window := range group.Windows {
		if window.ID != best.affected.ID {
			keep = append(keep, window.ID)
		}
	}
	return Suggestion{
		ActionKey:     fmt.Sprintf("micro-shift-%d-%s", best.affected.ID, signedOffsetKey(best.offset)),
		ActionType:    MicroShiftActionType,
		Title:         fmt.Sprintf("Micro-stagger window #%d by %s", best.affected.ID, shiftLabel(shiftMinutes)),
		Rationale:     "Keeps satellite, band and contact duration unchanged and moves only the start time within fifteen minutes while honouring the station slew buffer.",
		KeepWindowIDs: keep,
		MoveWindowIDs: []uint{best.affected.ID},
		ShiftWindowID: &best.affected.ID,
		ShiftMinutes:  shiftMinutes,
		OriginalStart: best.affected.StartAt,
		OriginalEnd:   best.affected.EndAt,
		ShiftedStart:  best.startAt,
		ShiftedEnd:    best.endAt,
		Score:         Score(generator.weights, 0, 0, best.affected.DurationSec(), best.margin),
	}, true
}

// nearestMicroSlot scans -15..-1 and +1..+15 minutes in distance order (the
// earlier slot wins a tie) and returns the first slot that is feasible; the
// returned margin is the antenna headroom at the shifted position.
func (generator *CandidateGenerator) nearestMicroSlot(affected model.ContactWindow, station model.GroundStation, buffer time.Duration) (microShiftCandidate, bool) {
	others := make([]model.ContactWindow, 0, len(generator.allWindows))
	for _, window := range generator.allWindows {
		if window.ID == affected.ID || window.StationID != station.ID || window.WindowStatus == constants.WindowStatusCancelled {
			continue
		}
		others = append(others, window)
	}
	duration := affected.EndAt.Sub(affected.StartAt)
	for minutes := 1; minutes <= int(MicroShiftLimit/time.Minute); minutes++ {
		for _, sign := range []int{-1, 1} {
			offset := time.Duration(sign*minutes) * microShiftStep
			startAt := affected.StartAt.Add(offset)
			shifted := affected
			shifted.ID = affected.ID
			shifted.StartAt = startAt
			shifted.EndAt = startAt.Add(duration)
			margin, feasible := microSlotFeasible(shifted, others, station, buffer)
			if !feasible {
				continue
			}
			return microShiftCandidate{affected: affected, offset: offset, margin: margin, startAt: startAt, endAt: shifted.EndAt}, true
		}
	}
	return microShiftCandidate{}, false
}

// microSlotFeasible verifies three constraints for a shifted window:
//  1. no other same-satellite contact overlaps (satellite capacity is one);
//  2. the station stays within antenna capacity on raw intervals;
//  3. the station stays within antenna capacity once every contact is expanded
//     by the station slew buffer.
//
// The returned margin counts free antenna channels at the shifted position.
func microSlotFeasible(shifted model.ContactWindow, others []model.ContactWindow, station model.GroundStation, buffer time.Duration) (int, bool) {
	for _, other := range others {
		if other.SatelliteID != shifted.SatelliteID {
			continue
		}
		if Overlaps(BuildInterval(shifted, 0, 0), BuildInterval(other, 0, 0)) {
			return 0, false
		}
	}
	stationWindows := make([]model.ContactWindow, 0, len(others)+1)
	stationWindows = append(stationWindows, shifted)
	for _, other := range others {
		stationWindows = append(stationWindows, other)
	}
	if groups := SweepCapacity(stationWindows, station.AntennaCount, 0); shiftInAnyGroup(shifted.ID, groups) {
		return 0, false
	}
	if groups := SweepCapacity(stationWindows, station.AntennaCount, buffer); shiftInAnyGroup(shifted.ID, groups) {
		return 0, false
	}
	peak := 0
	for _, other := range others {
		if Overlaps(BuildInterval(shifted, 0, 0), BuildInterval(other, 0, 0)) {
			peak++
		}
	}
	margin := station.AntennaCount - peak - 1
	if margin < 0 {
		margin = 0
	}
	return margin, true
}

func shiftInAnyGroup(shiftedID uint, groups [][]model.ContactWindow) bool {
	for _, group := range groups {
		for _, window := range group {
			if window.ID == shiftedID {
				return true
			}
		}
	}
	return false
}

func signedOffsetKey(offset time.Duration) string {
	minutes := int(offset / time.Minute)
	if minutes >= 0 {
		return fmt.Sprintf("p%02d", minutes)
	}
	return fmt.Sprintf("m%02d", -minutes)
}

func shiftLabel(minutes int) string {
	if minutes < 0 {
		return fmt.Sprintf("%d min earlier", -minutes)
	}
	return fmt.Sprintf("%d min later", minutes)
}
