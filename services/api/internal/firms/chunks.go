package firms

import "time"

// Chunk is a run of whole UTC days that one area request covers.
type Chunk struct {
	Start time.Time // midnight UTC on the first day
	Days  int
}

// End is midnight UTC after the last day, so a chunk covers [Start, End).
func (c Chunk) End() time.Time { return c.Start.AddDate(0, 0, c.Days) }

// Day returns midnight UTC on the calendar day of t.
func Day(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// PlanChunks splits the UTC days from first to last, both included, into
// chunks of at most maxDays. Days for which done returns true are left out,
// and a chunk never spans a day that is left out. done may be nil.
func PlanChunks(first, last time.Time, maxDays int, done func(day time.Time) bool) []Chunk {
	var chunks []Chunk
	if maxDays < 1 {
		return nil
	}
	var current *Chunk
	for day, end := Day(first), Day(last); !day.After(end); day = day.AddDate(0, 0, 1) {
		if done != nil && done(day) {
			current = nil
			continue
		}
		if current == nil || current.Days == maxDays {
			chunks = append(chunks, Chunk{Start: day})
			current = &chunks[len(chunks)-1]
		}
		current.Days++
	}
	return chunks
}
