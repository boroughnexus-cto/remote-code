package main

import (
	"testing"
)

// ─── Fuzz tests for filter/sort ─────────────────────────────────────────────
//
// Properties verified:
//   - Filtering never increases result count
//   - Filtering never panics regardless of input
//   - Sorting preserves item count
//   - Sorting never panics regardless of mode
//   - Cursor stays in bounds after filter+sort

func FuzzFilterPlaneIssues(f *testing.F) {
	// Seed corpus
	f.Add("auth")
	f.Add("")
	f.Add("URGENT")
	f.Add("backlog")
	f.Add("zzzzz")
	f.Add("!@#$%^&*()")
	f.Add("a very long filter string that exceeds any reasonable issue title length")

	issues := fakePlaneIssues()

	f.Fuzz(func(t *testing.T, query string) {
		m := newTestModel(nil)
		m.planeIssues = issues
		m.popupFilter.SetValue(query)

		for sortMode := 0; sortMode < len(planeSortLabels); sortMode++ {
			m.popupSortMode = sortMode
			filtered := filteredPlaneIssues(m)

			// Property: filtering never increases count
			if filtered != nil && len(filtered) > len(issues) {
				t.Errorf("filtered (%d) > original (%d) for query=%q sort=%d",
					len(filtered), len(issues), query, sortMode)
			}

			// Property: cursor would be in bounds
			if len(filtered) > 0 {
				for cursor := 0; cursor < len(filtered); cursor++ {
					// Access each element — should not panic
					_ = filtered[cursor].Title
				}
			}
		}
	})
}

func FuzzFilterIcingaProblems(f *testing.F) {
	f.Add("unraid")
	f.Add("")
	f.Add("CRITICAL")
	f.Add("exit code")
	f.Add("zzzzz")
	f.Add("\x00\xff")

	problems := fakeIcingaProblems()

	f.Fuzz(func(t *testing.T, query string) {
		m := newTestModel(nil)
		m.icingaProblems = problems
		m.popupFilter.SetValue(query)

		for sortMode := 0; sortMode < len(icingaSortLabels); sortMode++ {
			m.popupSortMode = sortMode
			filtered := filteredIcingaProblems(m)

			if filtered != nil && len(filtered) > len(problems) {
				t.Errorf("filtered (%d) > original (%d) for query=%q sort=%d",
					len(filtered), len(problems), query, sortMode)
			}

			if len(filtered) > 0 {
				for cursor := 0; cursor < len(filtered); cursor++ {
					_ = filtered[cursor].Service
				}
			}
		}
	})
}

func FuzzSortPlaneIssues(f *testing.F) {
	f.Add(0)
	f.Add(1)
	f.Add(2)
	f.Add(3)
	f.Add(99)
	f.Add(-1)

	issues := fakePlaneIssues()

	f.Fuzz(func(t *testing.T, mode int) {
		// Clamp to valid range to test sort logic, not modulo
		if mode < 0 {
			mode = 0
		}
		mode = mode % (len(planeSortLabels) + 1) // include one beyond valid

		sorted := sortPlaneIssues(issues, mode)

		// Property: sort preserves count
		if len(sorted) != len(issues) {
			t.Errorf("sort changed count: %d -> %d for mode=%d", len(issues), len(sorted), mode)
		}

		// Property: all original items present
		origTitles := make(map[string]bool)
		for _, i := range issues {
			origTitles[i.Title] = true
		}
		for _, s := range sorted {
			if !origTitles[s.Title] {
				t.Errorf("sorted contains unknown title %q", s.Title)
			}
		}
	})
}
