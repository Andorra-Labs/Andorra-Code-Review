package dedup

import "unicode/utf8"

// jaroWinkler returns a similarity score in [0,1] between two strings.
// 1.0 is identical, 0.0 is no common runes.
//
// This is a minimal, dependency-free implementation suitable for short
// finding titles (< 200 chars). For very long strings consider an external
// library.
func jaroWinkler(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}
	r1, r2 := []rune(s1), []rune(s2)
	l1, l2 := len(r1), len(r2)
	if l1 == 0 || l2 == 0 {
		return 0.0
	}

	matchWindow := max(l1, l2)/2 - 1
	if matchWindow < 0 {
		matchWindow = 0
	}

	s1Matches := make([]bool, l1)
	s2Matches := make([]bool, l2)

	matches := 0
	for i := 0; i < l1; i++ {
		start := i - matchWindow
		if start < 0 {
			start = 0
		}
		end := i + matchWindow + 1
		if end > l2 {
			end = l2
		}
		for j := start; j < end; j++ {
			if s2Matches[j] {
				continue
			}
			if r1[i] != r2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0.0
	}

	transpositions := 0
	k := 0
	for i := 0; i < l1; i++ {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if r1[i] != r2[k] {
			transpositions++
		}
		k++
	}

	mFloat := float64(matches)
	jaro := (mFloat/float64(l1) + mFloat/float64(l2) + (mFloat-float64(transpositions)/2)/mFloat) / 3.0

	// Winkler boost for common prefix up to 4 runes.
	prefix := 0
	limit := min(4, min(l1, l2))
	for i := 0; i < limit; i++ {
		if r1[i] != r2[i] {
			break
		}
		prefix++
	}
	const p = 0.1
	return jaro + float64(prefix)*p*(1.0-jaro)
}

// runeLen avoids re-decoding when we only need a count.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
