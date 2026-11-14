package bsdiff

// suffixArray is the start of every suffix of b in lexicographic order (a
// suffix before the longer ones it begins), by induced sorting (SA-IS: Nong,
// Zhang and Chan, "Two Efficient Algorithms for Linear Time Suffix Array
// Construction", 2011).
func suffixArray(b []byte) []int32 {
	if len(b) == 0 {
		return nil
	}
	t := make([]int32, len(b)+1)
	for i, c := range b {
		t[i] = int32(c) + 1
	}
	sa := make([]int32, len(t))
	sais(t, sa, 257)
	return sa[1:]
}

// sais sorts the suffixes of t into sa. t ends in its only 0 and its
// symbols are below k.
func sais(t, sa []int32, k int) {
	n := len(t)
	stype := make([]bool, n)
	stype[n-1] = true
	for i := n - 2; i >= 0; i-- {
		stype[i] = t[i] < t[i+1] || t[i] == t[i+1] && stype[i+1]
	}
	lms := func(i int) bool { return i > 0 && stype[i] && !stype[i-1] }
	count := make([]int32, k)
	for _, c := range t {
		count[c]++
	}
	bkt := make([]int32, k)
	ends := func() {
		var sum int32
		for i, c := range count {
			sum += c
			bkt[i] = sum
		}
	}
	heads := func() {
		var sum int32
		for i, c := range count {
			bkt[i] = sum
			sum += c
		}
	}
	induce := func() {
		heads()
		for i := 0; i < n; i++ {
			if j := sa[i] - 1; sa[i] > 0 && !stype[j] {
				sa[bkt[t[j]]] = j
				bkt[t[j]]++
			}
		}
		ends()
		for i := n - 1; i >= 0; i-- {
			if j := sa[i] - 1; sa[i] > 0 && stype[j] {
				bkt[t[j]]--
				sa[bkt[t[j]]] = j
			}
		}
	}

	// The LMS substrings, sorted by inducing from the LMS positions.
	for i := range sa {
		sa[i] = -1
	}
	ends()
	for i := 1; i < n; i++ {
		if lms(i) {
			bkt[t[i]]--
			sa[bkt[t[i]]] = int32(i)
		}
	}
	induce()

	// Name them in that order (equal substrings, equal names) and sort the
	// string of names, recursively while two are equal.
	n1 := 0
	for i := 0; i < n; i++ {
		if lms(int(sa[i])) {
			sa[n1] = sa[i]
			n1++
		}
	}
	for i := n1; i < n; i++ {
		sa[i] = -1
	}
	same := func(a, b int) bool {
		for d := 0; ; d++ {
			if d > 0 && (lms(a+d) || lms(b+d)) {
				return lms(a+d) && lms(b+d) && t[a+d] == t[b+d]
			}
			if t[a+d] != t[b+d] || stype[a+d] != stype[b+d] {
				return false
			}
		}
	}
	names := int32(0)
	for i := 0; i < n1; i++ {
		p := int(sa[i])
		if i == 0 || p == n-1 || int(sa[i-1]) == n-1 || !same(int(sa[i-1]), p) {
			names++
		}
		sa[n1+p/2] = names - 1
	}
	j := n - 1
	for i := n - 1; i >= n1; i-- {
		if sa[i] >= 0 {
			sa[j] = sa[i]
			j--
		}
	}
	s1, sa1 := sa[n-n1:], sa[:n1]
	if int(names) < n1 {
		sais(s1, sa1, int(names))
	} else {
		for i, c := range s1 {
			sa1[c] = int32(i)
		}
	}

	// The LMS suffixes in their sorted order, at their buckets' ends, induce
	// all the others.
	j = 0
	for i := 1; i < n; i++ {
		if lms(i) {
			s1[j] = int32(i)
			j++
		}
	}
	for i := range sa1 {
		sa1[i] = s1[sa1[i]]
	}
	for i := n1; i < n; i++ {
		sa[i] = -1
	}
	ends()
	for i := n1 - 1; i >= 0; i-- {
		p := sa[i]
		sa[i] = -1
		bkt[t[p]]--
		sa[bkt[t[p]]] = p
	}
	induce()
}
