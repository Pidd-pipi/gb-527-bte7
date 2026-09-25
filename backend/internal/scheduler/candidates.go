package scheduler

import (
	"fmt"
	"math"
	"sort"
	"time"

	"satellite-contact-window-deconfliction/backend/internal/constants"
	"satellite-contact-window-deconfliction/backend/internal/model"
)

type CandidateGenerator struct {
	weights    Weights
	stations   map[uint]model.GroundStation
	satellites map[uint]model.SatelliteAsset
	allWindows []model.ContactWindow
}

func NewCandidateGenerator(weights Weights, stations []model.GroundStation, satellites []model.SatelliteAsset, windows []model.ContactWindow) *CandidateGenerator {
	stationMap := map[uint]model.GroundStation{}
	assetMap := map[uint]model.SatelliteAsset{}
	for _, station := range stations {
		stationMap[station.ID] = station
	}
	for _, asset := range satellites {
		assetMap[asset.ID] = asset
	}
	return &CandidateGenerator{weights: weights, stations: stationMap, satellites: assetMap, allWindows: windows}
}

func (generator *CandidateGenerator) Generate(group ConflictGroup) []Suggestion {
	suggestions := []Suggestion{generator.keepHighest(group)}
	if alternative, ok := generator.alternateWindow(group); ok {
		suggestions = append(suggestions, alternative)
	}
	if relocation, ok := generator.compatibleStation(group); ok {
		suggestions = append(suggestions, relocation)
	}
	if shift, ok := generator.timeShift(group); ok {
		suggestions = append(suggestions, shift)
	}
	suggestions = append(suggestions, generator.manual(group))
	StableSortSuggestions(suggestions)
	return suggestions
}

func (generator *CandidateGenerator) keepHighest(group ConflictGroup) Suggestion {
	windows := append([]model.ContactWindow(nil), group.Windows...)
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Locked != windows[j].Locked {
			return windows[i].Locked
		}
		left := float64(windows[i].Priority) + generator.satellites[windows[i].SatelliteID].PriorityWeight
		right := float64(windows[j].Priority) + generator.satellites[windows[j].SatelliteID].PriorityWeight
		if left != right {
			return left > right
		}
		if windows[i].DurationSec() != windows[j].DurationSec() {
			return windows[i].DurationSec() > windows[j].DurationSec()
		}
		return windows[i].ID < windows[j].ID
	})
	keep := []uint{windows[0].ID}
	move := make([]uint, 0)
	priorityLoss := 0.0
	duration := windows[0].DurationSec()
	for _, window := range windows[1:] {
		if window.Locked {
			keep = append(keep, window.ID)
			continue
		}
		move = append(move, window.ID)
		priorityLoss += float64(window.Priority) + generator.satellites[window.SatelliteID].PriorityWeight
	}
	requiresManual := len(keep) > maxInt(1, group.Capacity) || group.ConflictType == constants.ConflictTypeDurationShortfall || group.ConflictType == constants.ConflictTypeBandMismatch
	return Suggestion{
		ActionKey: fmt.Sprintf("keep-priority-%d", windows[0].ID), ActionType: "keep_high_priority",
		Title:         fmt.Sprintf("Keep window #%d on the current plan", windows[0].ID),
		Rationale:     "Ranks locked windows first, then combines request priority, satellite weight, duration, and stable ID order.",
		KeepWindowIDs: keep, MoveWindowIDs: move, RequiresManual: requiresManual,
		Score: Score(generator.weights, priorityLoss, 0, duration, group.Capacity-len(keep)),
	}
}

func (generator *CandidateGenerator) alternateWindow(group ConflictGroup) (Suggestion, bool) {
	member := map[uint]bool{}
	for _, window := range group.Windows {
		member[window.ID] = true
	}
	for _, affected := range group.Windows {
		if affected.Locked {
			continue
		}
		alternatives := make([]model.ContactWindow, 0)
		for _, candidate := range generator.allWindows {
			if member[candidate.ID] || candidate.SatelliteID != affected.SatelliteID || candidate.SourceVersion != affected.SourceVersion || candidate.WindowStatus == constants.WindowStatusCancelled {
				continue
			}
			if !generator.alternateAvailable(candidate, affected, member) {
				continue
			}
			alternatives = append(alternatives, candidate)
		}
		sort.SliceStable(alternatives, func(i, j int) bool {
			di := absDuration(alternatives[i].StartAt.Sub(affected.StartAt))
			dj := absDuration(alternatives[j].StartAt.Sub(affected.StartAt))
			if di != dj {
				return di < dj
			}
			return alternatives[i].ID < alternatives[j].ID
		})
		if len(alternatives) == 0 {
			continue
		}
		alternate := alternatives[0]
		loss := math.Max(0, float64(affected.Priority-alternate.Priority))
		return Suggestion{
			ActionKey: fmt.Sprintf("alternate-%d-for-%d", alternate.ID, affected.ID), ActionType: "use_alternate_window",
			Title:         fmt.Sprintf("Use source-matched window #%d", alternate.ID),
			Rationale:     "Selects the nearest stable opportunity produced by the same offline orbit source version.",
			KeepWindowIDs: []uint{alternate.ID}, MoveWindowIDs: []uint{affected.ID}, AlternateWindowID: &alternate.ID,
			Score: Score(generator.weights, loss, 0, alternate.DurationSec(), 0),
		}, true
	}
	return Suggestion{}, false
}

func (generator *CandidateGenerator) compatibleStation(group ConflictGroup) (Suggestion, bool) {
	if group.ConflictType == constants.ConflictTypeDurationShortfall || group.ConflictType == constants.ConflictTypeSatelliteOverlap {
		return Suggestion{}, false
	}
	for _, affected := range group.Windows {
		if affected.Locked {
			continue
		}
		asset, assetOK := generator.satellites[affected.SatelliteID]
		if !assetOK || !containsBand(asset.SupportedBandsJSON, affected.Band) {
			continue
		}
		current, ok := generator.stations[affected.StationID]
		if !ok {
			continue
		}
		candidates := make([]model.GroundStation, 0)
		for _, station := range generator.stations {
			if station.ID == current.ID || station.StationStatus != "active" || !containsBand(station.SupportedBandsJSON, affected.Band) {
				continue
			}
			if generator.concurrentAt(station.ID, affected) >= station.AntennaCount {
				continue
			}
			candidates = append(candidates, station)
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			di := haversine(current.Latitude, current.Longitude, candidates[i].Latitude, candidates[i].Longitude)
			dj := haversine(current.Latitude, current.Longitude, candidates[j].Latitude, candidates[j].Longitude)
			if di != dj {
				return di < dj
			}
			return candidates[i].StationCode < candidates[j].StationCode
		})
		if len(candidates) == 0 {
			continue
		}
		target := candidates[0]
		distance := haversine(current.Latitude, current.Longitude, target.Latitude, target.Longitude)
		margin := target.AntennaCount - generator.concurrentAt(target.ID, affected) - 1
		return Suggestion{
			ActionKey: fmt.Sprintf("station-%d-window-%d", target.ID, affected.ID), ActionType: "assign_compatible_station",
			Title:         fmt.Sprintf("Evaluate %s for window #%d", target.StationCode, affected.ID),
			Rationale:     "Chooses the nearest active band-compatible station with capacity remaining in the same interval.",
			MoveWindowIDs: []uint{affected.ID}, TargetStationID: &target.ID,
			Score: Score(generator.weights, 0, distance, affected.DurationSec(), margin),
		}, true
	}
	return Suggestion{}, false
}

func (generator *CandidateGenerator) alternateAvailable(candidate, affected model.ContactWindow, groupMembers map[uint]bool) bool {
	station, ok := generator.stations[candidate.StationID]
	if !ok || station.StationStatus != "active" || !containsBand(station.SupportedBandsJSON, candidate.Band) {
		return false
	}
	asset, ok := generator.satellites[candidate.SatelliteID]
	if !ok || !containsBand(asset.SupportedBandsJSON, candidate.Band) {
		return false
	}
	stationConcurrency := 0
	for _, existing := range generator.allWindows {
		if existing.ID == candidate.ID || existing.ID == affected.ID || groupMembers[existing.ID] || existing.WindowStatus == constants.WindowStatusCancelled {
			continue
		}
		overlaps := existing.StartAt.Before(candidate.EndAt) && candidate.StartAt.Before(existing.EndAt)
		if !overlaps {
			continue
		}
		if existing.SatelliteID == candidate.SatelliteID {
			return false
		}
		if existing.StationID == candidate.StationID {
			stationConcurrency++
		}
	}
	return stationConcurrency < station.AntennaCount
}

const (
	maxTimeShiftMinutes  = 15
	timeShiftStepMinutes = 1
)

// timeShift keeps satellite, band, and contact duration unchanged and searches
// ±maxTimeShiftMinutes around the original start for a gap that clears station
// capacity, the station slew buffer, and same-satellite overlap. Locked windows
// never move; conflict types that a pure time shift cannot repair are skipped.
func (generator *CandidateGenerator) timeShift(group ConflictGroup) (Suggestion, bool) {
	if group.ConflictType == constants.ConflictTypeBandMismatch || group.ConflictType == constants.ConflictTypeDurationShortfall {
		return Suggestion{}, false
	}
	for _, affected := range group.Windows {
		if affected.Locked {
			continue
		}
		station, ok := generator.stations[affected.StationID]
		if !ok || station.StationStatus != "active" {
			continue
		}
		buffer := time.Duration(station.SlewBufferSec) * time.Second
		duration := affected.EndAt.Sub(affected.StartAt)
		for offset := timeShiftStepMinutes; offset <= maxTimeShiftMinutes; offset += timeShiftStepMinutes {
			for _, direction := range []time.Duration{-1, 1} {
				shift := direction * time.Duration(offset) * time.Minute
				start := affected.StartAt.Add(shift)
				end := start.Add(duration)
				concurrency := generator.stationConcurrencyAt(affected, station.ID, start, end, buffer)
				if concurrency >= station.AntennaCount || !generator.satelliteFree(affected, start, end) {
					continue
				}
				minutes := int(shift.Minutes())
				originalStart, originalEnd := affected.StartAt, affected.EndAt
				keep := make([]uint, 0, len(group.Windows)-1)
				for _, window := range group.Windows {
					if window.ID != affected.ID {
						keep = append(keep, window.ID)
					}
				}
				return Suggestion{
					ActionKey: fmt.Sprintf("timeshift-%+d-window-%d", minutes, affected.ID), ActionType: "shift_window_time",
					Title:         fmt.Sprintf("Shift window #%d by %+d min on the same station", affected.ID, minutes),
					Rationale:     "Keeps satellite, band, and contact duration unchanged; picks the smallest ±15 minute offset that clears capacity and the station slew buffer.",
					KeepWindowIDs: keep, MoveWindowIDs: []uint{affected.ID},
					ShiftMinutes: minutes, OriginalStartAt: &originalStart, OriginalEndAt: &originalEnd, ShiftedStartAt: &start, ShiftedEndAt: &end,
					Score: Score(generator.weights, 0, 0, affected.DurationSec(), station.AntennaCount-concurrency-1),
				}, true
			}
		}
	}
	return Suggestion{}, false
}

func (generator *CandidateGenerator) stationConcurrencyAt(affected model.ContactWindow, stationID uint, start, end time.Time, buffer time.Duration) int {
	count := 0
	shifted := Interval{Window: affected, Start: start.Add(-buffer), End: end.Add(buffer)}
	for _, existing := range generator.allWindows {
		if existing.ID == affected.ID || existing.StationID != stationID || existing.WindowStatus == constants.WindowStatusCancelled {
			continue
		}
		if Overlaps(BuildInterval(existing, buffer, buffer), shifted) {
			count++
		}
	}
	return count
}

func (generator *CandidateGenerator) satelliteFree(affected model.ContactWindow, start, end time.Time) bool {
	for _, existing := range generator.allWindows {
		if existing.ID == affected.ID || existing.SatelliteID != affected.SatelliteID || existing.WindowStatus == constants.WindowStatusCancelled {
			continue
		}
		if existing.StartAt.Before(end) && start.Before(existing.EndAt) {
			return false
		}
	}
	return true
}

func (generator *CandidateGenerator) manual(group ConflictGroup) Suggestion {	duration := 0
	ids := make([]uint, 0, len(group.Windows))
	locked := false
	for _, window := range group.Windows {
		duration += window.DurationSec()
		ids = append(ids, window.ID)
		locked = locked || window.Locked
	}
	rationale := "Preserves every candidate and records the conflict for an operator-authored planning decision."
	if locked {
		rationale = "At least one affected window is locked; automated movement is prohibited and human review is required."
	}
	return Suggestion{
		ActionKey: "manual-review-" + group.Key, ActionType: "manual_review", Title: "Keep unresolved for manual planning",
		Rationale: rationale, KeepWindowIDs: ids, RequiresManual: true, Score: Score(generator.weights, 0, 0, duration, 0),
	}
}

func (generator *CandidateGenerator) concurrentAt(stationID uint, target model.ContactWindow) int {
	count := 0
	for _, window := range generator.allWindows {
		if window.StationID != stationID || window.ID == target.ID || window.WindowStatus == constants.WindowStatusCancelled {
			continue
		}
		if window.StartAt.Before(target.EndAt) && target.StartAt.Before(window.EndAt) {
			count++
		}
	}
	return count
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const radius = 6371.0
	toRadians := func(value float64) float64 { return value * math.Pi / 180 }
	latDelta, lonDelta := toRadians(lat2-lat1), toRadians(lon2-lon1)
	a := math.Sin(latDelta/2)*math.Sin(latDelta/2) + math.Cos(toRadians(lat1))*math.Cos(toRadians(lat2))*math.Sin(lonDelta/2)*math.Sin(lonDelta/2)
	return radius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
