package main

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestReportWindow(t *testing.T) {
	// 20:45 in India on 15 March 2026.
	now := time.Date(2026, 3, 15, 15, 15, 0, 0, time.UTC)
	hours := func(n int) *int { return &n }
	for _, tc := range []struct {
		name       string
		req        reportRequest
		start, end time.Time
		days       int
		partial    bool
		problem    string
	}{
		{name: "past day", req: reportRequest{From: "2026-03-14"}, days: 1,
			start: time.Date(2026, 3, 13, 18, 30, 0, 0, time.UTC), end: time.Date(2026, 3, 14, 18, 29, 59, 999999000, time.UTC)},
		{name: "today ends now", req: reportRequest{From: "2026-03-15"}, days: 1, partial: true,
			start: time.Date(2026, 3, 14, 18, 30, 0, 0, time.UTC), end: now},
		{name: "week", req: reportRequest{From: "2026-03-09", To: "2026-03-15"}, days: 7, partial: true,
			start: time.Date(2026, 3, 8, 18, 30, 0, 0, time.UTC), end: now},
		{name: "past hours", req: reportRequest{Hours: hours(24)}, days: 2, partial: true,
			start: now.Add(-24 * time.Hour), end: now},
		{name: "longest", req: reportRequest{From: "2025-09-13", To: "2026-03-15"}, days: 184, partial: true,
			start: time.Date(2025, 9, 12, 18, 30, 0, 0, time.UTC), end: now},
		{name: "too long", req: reportRequest{From: "2025-09-12", To: "2026-03-15"}, problem: "A report covers at most 184 days. Choose a shorter period."},
		{name: "future", req: reportRequest{From: "2026-03-16"}, problem: "The period cannot end after today."},
		{name: "backwards", req: reportRequest{From: "2026-03-15", To: "2026-03-14"}, problem: "The period must end on or after the day it starts."},
		{name: "not a date", req: reportRequest{From: "15/03/2026"}, problem: "Give from, and optionally to, as dates such as 2026-03-15, or hours."},
		{name: "no day", req: reportRequest{From: "2026-02-30"}, problem: "Give from, and optionally to, as dates such as 2026-03-15, or hours."},
		{name: "missing", req: reportRequest{}, problem: "Give from, and optionally to, as dates such as 2026-03-15, or hours."},
		{name: "both", req: reportRequest{From: "2026-03-15", Hours: hours(24)}, problem: "Give either dates or hours, not both."},
		{name: "too many hours", req: reportRequest{Hours: hours(721)}, problem: "hours must be an integer from 1 to 720."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			period, err := reportWindow(tc.req, now)
			var problem *reportProblem
			if tc.problem != "" {
				if !errors.As(err, &problem) || problem.message != tc.problem || problem.status != 400 {
					t.Fatalf("got %v, want %q", err, tc.problem)
				}
				return
			}
			if err != nil || !period.Start.Equal(tc.start) || !period.End.Equal(tc.end) || period.Days != tc.days ||
				period.Partial != tc.partial || period.TimeZone != "Asia/Kolkata" {
				t.Fatalf("got %+v, %v", period, err)
			}
		})
	}
}

func TestReadReportQuery(t *testing.T) {
	q, _ := url.ParseQuery("from=2026-03-09&to=2026-03-15&unitPath=ds.c1.d1&hierarchy=territorial")
	req := readReportQuery(q)
	if req.From != "2026-03-09" || req.To != "2026-03-15" || req.UnitPath == nil || *req.UnitPath != "ds.c1.d1" ||
		req.Hierarchy != "territorial" || req.Hours != nil {
		t.Fatalf("got %+v", req)
	}
	q, _ = url.ParseQuery("hours=x&unitPath=")
	req = readReportQuery(q)
	if req.Hours == nil || *req.Hours != -1 || req.UnitPath != nil {
		t.Fatalf("got %+v", req)
	}
	if _, err := reportWindow(req, time.Now()); err == nil {
		t.Fatal("a bad hours value was accepted")
	}
}
